package parser

import (
	"github.com/baslentfert/jsc2go/ident"
)

// ModuleAnalyzer (ModuleAnalyzer.cpp, NodesAnalyzeModule.cpp): collects the
// static module record fields of ParseModule (16.2.1.7.1) from a parsed
// module — requested modules, import entries and the local, indirect and
// star export entries.

// ModuleRequest is a ModuleRequest Record: a specifier plus the value of
// the "type" import attribute ("" when absent).
type ModuleRequest struct {
	Specifier string
	Type      string
}

// ImportEntry is an ImportEntry Record. ImportName is
// ident.StarNamespacePrivateName for `import * as ns`.
type ImportEntry struct {
	Request    ModuleRequest
	ImportName ident.Identifier
	LocalName  ident.Identifier
	Deferred   bool
}

// IsNamespace reports whether the entry imports the namespace object.
func (e *ImportEntry) IsNamespace() bool { return e.ImportName == ident.StarNamespacePrivateName }

// ExportEntry is an ExportEntry Record. For indirect exports ImportName is
// ident.StarNamespacePrivateName for `export * as ns from "m"`.
type ExportEntry struct {
	ExportName ident.Identifier
	LocalName  ident.Identifier // local exports
	ImportName ident.Identifier // indirect exports
	Request    ModuleRequest    // indirect and star exports
}

// ModuleInfo is the analysis result.
type ModuleInfo struct {
	RequestedModules []ModuleRequest
	ImportEntries    []ImportEntry
	LocalExports     []ExportEntry
	IndirectExports  []ExportEntry
	StarExports      []ExportEntry
	HasTLA           bool
}

// ModuleAnalysisError is an error found while analyzing (an unsupported
// import attribute); IsTypeError distinguishes TypeErrors from SyntaxErrors.
type ModuleAnalysisError struct {
	Message     string
	IsTypeError bool
}

func (e *ModuleAnalysisError) Error() string { return e.Message }

// SupportedImportTypes are the values accepted for the "type" import attribute.
var SupportedImportTypes = map[string]bool{"json": true, "text": true}

// ImportAttributesRequest validates import attributes and returns the
// request; it is shared with dynamic import().
func ImportAttributesRequest(specifier string, attributes []ImportAttribute) (ModuleRequest, *ModuleAnalysisError) {
	req := ModuleRequest{Specifier: specifier}
	for _, a := range attributes {
		if a.Key.String() != "type" {
			return req, &ModuleAnalysisError{Message: "Import attribute \"" + a.Key.String() + "\" is not supported"}
		}
	}
	for _, a := range attributes {
		t := a.Value.String()
		if !SupportedImportTypes[t] {
			return req, &ModuleAnalysisError{Message: "Import attribute type \"" + t + "\" is not valid", IsTypeError: true}
		}
		req.Type = t
	}
	return req, nil
}

func requestFor(name *ModuleNameNode, attrs *ImportAttributesListNode) (ModuleRequest, *ModuleAnalysisError) {
	var list []ImportAttribute
	if attrs != nil {
		list = attrs.Attributes
	}
	return ImportAttributesRequest(name.ModuleName.String(), list)
}

// AnalyzeModule analyzes a parsed module.
func AnalyzeModule(m *ModuleProgramNode) (*ModuleInfo, *ModuleAnalysisError) {
	info := &ModuleInfo{HasTLA: m.UsesAwait}
	seen := map[ModuleRequest]bool{}
	appendRequest := func(r ModuleRequest) {
		if !seen[r] {
			seen[r] = true
			info.RequestedModules = append(info.RequestedModules, r)
		}
	}
	var indirect []ExportEntry
	if m.Statements != nil {
		for _, st := range m.Statements.Statements {
			switch n := st.(type) {
			case *ImportDeclarationNode:
				req, err := requestFor(n.ModuleName, n.AttributesList)
				if err != nil {
					return nil, err
				}
				appendRequest(req)
				if n.SpecifierList == nil {
					continue
				}
				for _, s := range n.SpecifierList.Specifiers {
					info.ImportEntries = append(info.ImportEntries, ImportEntry{Request: req, ImportName: s.ImportedName,
						LocalName: s.LocalName, Deferred: n.Type == ImportTypeDeferred})
				}
			case *ExportAllDeclarationNode:
				req, err := requestFor(n.ModuleName, n.AttributesList)
				if err != nil {
					return nil, err
				}
				appendRequest(req)
				info.StarExports = append(info.StarExports, ExportEntry{Request: req})
			case *ExportNamedDeclarationNode:
				if n.ModuleName == nil {
					continue
				}
				req, err := requestFor(n.ModuleName, n.AttributesList)
				if err != nil {
					return nil, err
				}
				appendRequest(req)
				for _, s := range n.SpecifierList.Specifiers {
					indirect = append(indirect, ExportEntry{ExportName: s.ExportedName, ImportName: s.LocalName, Request: req})
				}
			}
		}
	}

	exportedAs := map[ident.Identifier][]ident.Identifier{}
	if m.ModuleScopeData != nil {
		for local, names := range m.ModuleScopeData.ExportedBindings {
			exportedAs[local] = names
		}
	}
	importFor := func(local ident.Identifier) *ImportEntry {
		for i := range info.ImportEntries {
			if info.ImportEntries[i].LocalName == local {
				return &info.ImportEntries[i]
			}
		}
		return nil
	}
	exportVariable := func(local ident.Identifier, e *VariableEnvironmentEntry) bool {
		if !e.IsExported() {
			return true
		}
		ie := importFor(local)
		for _, name := range exportedAs[local] {
			switch {
			case !e.IsImported() || ie == nil:
				info.LocalExports = append(info.LocalExports, ExportEntry{ExportName: name, LocalName: local})
			// A re-exported namespace import (import * as ns; export {ns}) resolves
			// to the namespace itself, like JSC, so that two such re-exports of the
			// same module are not ambiguous.
			default:
				info.IndirectExports = append(info.IndirectExports, ExportEntry{ExportName: name, ImportName: ie.ImportName, Request: ie.Request})
			}
		}
		return true
	}
	m.VarDeclarations.All(exportVariable)
	m.LexicalVariables.All(exportVariable)
	info.IndirectExports = append(info.IndirectExports, indirect...)
	return info, nil
}
