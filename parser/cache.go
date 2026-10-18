package parser

import (
	"sync"

	"github.com/baslentfert/jsc2go/ident"
)

// Port of SourceProviderCache / SourceProviderCacheItem. When a function body
// is parsed for the first time, what the enclosing scopes need to know about
// it is recorded here, keyed by the offset of its parameters. A later parse of
// the same source (e.g. reparsing an outer function) skips the body entirely.

type sourceProviderCacheItem struct {
	needsFullActivation        bool
	endFunctionOffset          int
	usesEval                   bool
	lastTokenStartOffset       int
	lexicallyScopedFeatures    LexicallyScopedFeatures
	lastTokenEndOffset         int
	expectedSuperBinding       SuperBinding
	parameterCount             int
	needsSuperBinding          bool
	isBodyArrowExpression      bool
	tokenType                  TokenType
	innerArrowFunctionFeatures InnerArrowFunctionCodeFeatures
	constructorKind            ConstructorKind
	implementationVisibility   ImplementationVisibility
	usesImportMeta             bool
	usedVariables              []ident.Identifier
}

// endFunctionToken rebuilds the token that ended the function.
func (c *sourceProviderCacheItem) endFunctionToken() Token {
	t := Token{Type: CLOSEBRACE, Start: TextPosition(c.lastTokenStartOffset), End: TextPosition(c.lastTokenEndOffset)}
	if c.isBodyArrowExpression {
		t.Type = c.tokenType
	}
	return t
}

// sourceProviderCacheItemCreationParameters is filled in by parseFunctionInfo.
type sourceProviderCacheItemCreationParameters struct {
	lastTokenStartOffset       int
	lastTokenEndOffset         int
	endFunctionOffset          int
	parameterCount             int
	freeVariableCount          int
	lexicallyScopedFeatures    LexicallyScopedFeatures
	innerArrowFunctionFeatures InnerArrowFunctionCodeFeatures
	implementationVisibility   ImplementationVisibility
	// The scope's own free variables followed by captures from its parameter expressions.
	usedVariables         []ident.Identifier
	tokenType             TokenType
	constructorKind       ConstructorKind
	expectedSuperBinding  SuperBinding
	needsFullActivation   bool
	usesEval              bool
	usesImportMeta        bool
	needsSuperBinding     bool
	isBodyArrowExpression bool
}

func (p *sourceProviderCacheItemCreationParameters) freeVariables() []ident.Identifier {
	return p.usedVariables[:p.freeVariableCount]
}

func newSourceProviderCacheItem(p *sourceProviderCacheItemCreationParameters) *sourceProviderCacheItem {
	return &sourceProviderCacheItem{
		needsFullActivation:        p.needsFullActivation,
		endFunctionOffset:          p.endFunctionOffset,
		usesEval:                   p.usesEval,
		lastTokenStartOffset:       p.lastTokenStartOffset,
		lexicallyScopedFeatures:    p.lexicallyScopedFeatures,
		lastTokenEndOffset:         p.lastTokenEndOffset,
		expectedSuperBinding:       p.expectedSuperBinding,
		parameterCount:             p.parameterCount,
		needsSuperBinding:          p.needsSuperBinding,
		isBodyArrowExpression:      p.isBodyArrowExpression,
		tokenType:                  p.tokenType,
		innerArrowFunctionFeatures: p.innerArrowFunctionFeatures,
		constructorKind:            p.constructorKind,
		implementationVisibility:   p.implementationVisibility,
		usesImportMeta:             p.usesImportMeta,
		usedVariables:              cloneIdents(p.usedVariables),
	}
}

// sourceProviderCache maps a function's parameters offset to its cache item.
type sourceProviderCache struct {
	mu    sync.Mutex
	items map[int]*sourceProviderCacheItem
}

func (c *sourceProviderCache) get(offset int) *sourceProviderCacheItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.items[offset]
}

func (c *sourceProviderCache) add(offset int, item *sourceProviderCacheItem) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = make(map[int]*sourceProviderCacheItem)
	}
	c.items[offset] = item
}

// Clear drops all cached function information for this source.
func (p *SourceProvider) ClearCache() {
	p.cache.mu.Lock()
	p.cache.items = nil
	p.cache.mu.Unlock()
}

// UseSourceProviderCache mirrors JSC's useSourceProviderCache option.
var UseSourceProviderCache = true
