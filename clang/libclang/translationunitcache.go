// +build !nolibclang

package libclang

import (
	"bytes"
	"code.google.com/p/log4go"
	"fmt"
	"github.com/quarnster/completion/content"
	"github.com/sbinet/go-clang"
	"reflect"
	"sync"
)

type (
	LockedTranslationUnit struct {
		sync.Mutex
		clang.TranslationUnit
		opts        []string
		opts_script string
	}

	tulut map[string]*LockedTranslationUnit

	workunit struct {
		filename       string
		options        []string
		options_script string
		on_done        func()
	}

	workqueue chan func()

	TranslationUnitCache struct {
		index   clang.Index
		lut     tulut
		parsing map[string]bool
		queue   workqueue
		sync.Mutex
	}
)

const (
	WORK_QUEUE_LEN      = 10
	index_parse_options = 0 // clang.TU_PrecompiledPreamble | clang.TU_CacheCompletionResults
)

func NewTranslationUnitCache() *TranslationUnitCache {
	t := &TranslationUnitCache{}
	t.index = clang.NewIndex(0, 1)
	t.lut = make(tulut)
	t.parsing = make(map[string]bool)
	t.queue = make(workqueue, WORK_QUEUE_LEN)
	return t
}

func (t *TranslationUnitCache) parse(w workunit) (ret *LockedTranslationUnit) {
	// TODO(q): SublimeClang marked the file as "busy". Is that needed?
	defer func() {
		t.Lock()
		defer t.Unlock()
		t.parsing[w.filename] = false
		// TODO(q): Is on_done really needed?
		if w.on_done != nil {
			w.on_done()
		}
	}()
	log4go.Debug("Parsing %s", w.filename)
	ret = t.GetTranslationUnit(w.filename, w.options, w.options_script, nil)
	log4go.Debug("Parsing %s done", w.filename)
	return ret
}

func (t *TranslationUnitCache) reparse(w workunit) {
	if tu := t.parse(w); tu != nil {
		tu.Lock()
		defer tu.Unlock()

		log4go.Debug("Reparsing %s", w.filename)
		tu.Reparse(0)
		log4go.Debug("Reparsing %s done", w.filename)
	}
}

func (t *TranslationUnitCache) addEx(w workunit) {
	t.Lock()
	defer t.Unlock()
	if _, ok := t.lut[w.filename]; !ok && !t.parsing[w.filename] {
		t.parsing[w.filename] = true
		t.queue <- func() { t.parse(w) }
	}
}

// TODO(q): respect unsaved_files
func (t *TranslationUnitCache) GetTranslationUnit(filename string, options []string, options_script string, unsaved_files map[string]string) *LockedTranslationUnit {
	t.Lock()
	if tu, ok := t.lut[filename]; !ok {
		t.Unlock()

		// TODO(q): SublimeClang executed opts_script and expand_path here
		log4go.Debug("Will compile file %s with the following options:\n%v", filename, options)
		if tu2 := t.index.Parse(filename, options, index_parse_options); true || tu2.IsValid() {
			tu = &LockedTranslationUnit{}
			tu.TranslationUnit = tu2
			t.Lock()
			defer t.Unlock()
			t.lut[filename] = tu
		} else {
			log4go.Warn("Failed to compile %s, %v", filename, tu2)
		}
		return tu
	} else {
		recompile := !reflect.DeepEqual(tu.opts, options) || tu.opts_script != options_script
		if recompile {
			// TODO: need to dispose the tu.. Who's responsible for its disposal?
			delete(t.lut, filename)
		}
		t.Unlock()
		if recompile {
			log4go.Debug("Options change detected. Will recompile %s", filename)
			t.addEx(workunit{filename, options, options_script, nil})
		}
		return tu
	}
	return nil
}

func (t *TranslationUnitCache) CompleteAt(args *content.CompleteAtArgs, res *content.CompletionResult) error {
	if len(args.Location.File.Contents) != 0 {
		return fmt.Errorf("Unfortunately completion of unsaved files is currently not supported")
	}
	// TODO: Use GetTranslationUnit
	if tu, ok := t.lut[args.Location.File.Name]; ok {
		cres := tu.CompleteAt(args.Location.File.Name, int(args.Location.Line), int(args.Location.Column), 0)
		if !cres.IsValid() {
			return fmt.Errorf("CompleteResults is not valid")
		}
		defer cres.Dispose()
		for _, r := range cres.Results() {
			var (
				buf bytes.Buffer
			)
			switch r.CursorKind {
			case clang.CK_StructDecl, clang.CK_TypedefDecl:
				for _, c := range r.CompletionString.Chunks() {
					buf.WriteString(c.Text())
				}
				var tt content.Type
				tt.Flags = content.FLAG_TYPE_CLASS
				tt.Name.Absolute = buf.String()
				res.Types = append(res.Types, tt)
			case clang.CK_FunctionDecl:
				var (
					m            content.Method
					paramstarted bool
					argCount     int
				)
				for _, c := range r.CompletionString.Chunks() {
					switch k := c.Kind(); k {
					case clang.CompletionChunk_ResultType:
						var v content.Variable
						v.Name.Relative = c.Text()
						m.Returns = append(m.Returns, v)
					case clang.CompletionChunk_Placeholder:
						var v content.Variable
						v.Type.Name.Relative = c.Text()
						v.Name.Relative = fmt.Sprintf("arg%d", argCount)
						argCount++
						m.Parameters = append(m.Parameters, v)
					case clang.CompletionChunk_LeftParen:
						paramstarted = true
					case clang.CompletionChunk_RightParen, clang.CompletionChunk_Comma:
					case clang.CompletionChunk_TypedText:
						if !paramstarted {
							buf.WriteString(c.Text())
						}
					default:
						log4go.Warn("Unimplemented CompletionChunkKind: %s", k)
					}
				}
				m.Name.Relative = buf.String()
				res.Methods = append(res.Methods, m)
			default:
				log4go.Warn("Unimplemented CursorKind: %s", r.CursorKind)
			}
		}
	}
	return nil
}