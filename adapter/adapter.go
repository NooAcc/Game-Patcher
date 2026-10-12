package adapter

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Context 描述一个待判断或待构建的文件变更。
type Context struct {
	OldDir  string
	NewDir  string
	RelPath string
	OldPath string
	NewPath string
	OldSize uint64
	NewSize uint64
	OldHash [32]byte
	NewHash [32]byte
	Options Options
}

// Options 是所有适配器共享的构建选项。适配器读取自己认识的键，忽略其他键。
type Options struct {
	Values map[string]string
}

const (
	// OptionSelected 控制本次构建使用哪个适配器。
	// 空值或 "none" 表示禁用全部适配器；其他值必须是已注册适配器名称。
	OptionSelected = "adapter.selected"
	SelectedNone   = "none"
)

// Described 是适配器可选实现的展示接口。
type Described interface {
	Description() string
}

// DisplayName 返回适配器的 TUI 展示名称。
func DisplayName(a Adapter) string {
	if d, ok := a.(Described); ok {
		if s := strings.TrimSpace(d.Description()); s != "" {
			return s
		}
	}
	return a.Name()
}

// MatchResult 描述适配器是否愿意处理一个文件。
type MatchResult struct {
	Matched    bool
	Confidence int
	Reason     string
}

// AdapterMatch 是匹配结果与对应适配器的组合。
type AdapterMatch struct {
	Adapter Adapter
	Result  MatchResult
}

// OpKind 是适配器层的中立操作类型。
type OpKind uint8

const (
	OpCopy OpKind = iota + 1
	OpLiteral
	OpCopyFrom
)

// Op 描述一段输出：从旧文件复制、写入新数据，或从其他文件复制。
type Op struct {
	Kind      OpKind
	OldOffset uint64
	Length    uint64
	SrcPath   string
	Data      []byte
}

// FileDelta 是适配器对单个文件给出的最终差异。
type FileDelta struct {
	OldHash [32]byte
	NewHash [32]byte
	OldSize uint64
	NewSize uint64
	Ops     []Op
}

// BuildContext 是适配器构建差异时的完整上下文。
type BuildContext struct {
	Context
}

// BuildResult 是适配器的构建结果。Accepted=false 表示调用方应回退通用差分。
type BuildResult struct {
	Delta    *FileDelta
	Accepted bool
	Reason   string
}

// Adapter 是引擎专用适配器接口。
type Adapter interface {
	Name() string
	Match(Context) MatchResult
	Build(BuildContext) (BuildResult, error)
}

// Registry 持有有序的适配器集合。
type Registry struct {
	mu       sync.RWMutex
	adapters []Adapter
}

func NewRegistry() *Registry { return &Registry{} }

// Register 追加一个适配器。重复名称会 panic，避免静默覆盖。
func (r *Registry) Register(a Adapter) {
	if a == nil {
		panic("adapter: nil adapter")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.adapters {
		if existing.Name() == a.Name() {
			panic(fmt.Sprintf("adapter: duplicate adapter %q", a.Name()))
		}
	}
	r.adapters = append(r.adapters, a)
}

// Reset 清空注册表，主要用于测试和显式重建适配器集。
func (r *Registry) Reset() {
	r.mu.Lock()
	r.adapters = nil
	r.mu.Unlock()
}

// Adapters 返回注册表当前适配器的副本。
func (r *Registry) Adapters() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Adapter, len(r.adapters))
	copy(out, r.adapters)
	return out
}

// Match 返回所有匹配适配器，按置信度降序、名称升序排列。
func (r *Registry) Match(ctx Context) []AdapterMatch {
	r.mu.RLock()
	adapters := make([]Adapter, len(r.adapters))
	copy(adapters, r.adapters)
	r.mu.RUnlock()

	selected := ""
	if ctx.Options.Values != nil {
		selected = strings.TrimSpace(ctx.Options.Values[OptionSelected])
	}
	if selected == "" || selected == SelectedNone {
		return nil
	}
	out := make([]AdapterMatch, 0, len(adapters))
	for _, a := range adapters {
		if a.Name() != selected {
			continue
		}
		res := a.Match(ctx)
		if res.Matched {
			out = append(out, AdapterMatch{Adapter: a, Result: res})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Result.Confidence != out[j].Result.Confidence {
			return out[i].Result.Confidence > out[j].Result.Confidence
		}
		return out[i].Adapter.Name() < out[j].Adapter.Name()
	})
	return out
}

// Build 依次询问匹配适配器，直到某个适配器接受为止。
// 全部拒绝时返回 Accepted=false、err=nil，调用方应回退通用差分。
func (r *Registry) Build(ctx BuildContext) (BuildResult, error) {
	matches := r.Match(ctx.Context)
	var declined []string
	for _, m := range matches {
		res, err := m.Adapter.Build(ctx)
		if err != nil {
			return BuildResult{}, fmt.Errorf("adapter %s: %w", m.Adapter.Name(), err)
		}
		if res.Accepted {
			return res, nil
		}
		if res.Reason != "" {
			declined = append(declined, m.Adapter.Name()+": "+res.Reason)
		}
	}
	reason := ""
	if len(declined) > 0 {
		reason = fmt.Sprintf("%v", declined)
	}
	return BuildResult{Accepted: false, Reason: reason}, nil
}

// DefaultRegistry 是构建流程默认使用的全局注册表。
var DefaultRegistry = NewRegistry()

func Register(a Adapter) { DefaultRegistry.Register(a) }

func Reset() { DefaultRegistry.Reset() }

func Match(ctx Context) []AdapterMatch { return DefaultRegistry.Match(ctx) }

func Build(ctx BuildContext) (BuildResult, error) { return DefaultRegistry.Build(ctx) }
