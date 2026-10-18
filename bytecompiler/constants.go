package bytecompiler

import (
	"math"
	"math/big"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
)

// The constant pool (BytecodeGenerator.cpp: addConstant*, emitLoad, ...).

// addConstant returns the index of id in the identifier table.
func (g *Generator) addConstant(id ident.Identifier) uint32 {
	if i, ok := g.identifierMap[id]; ok {
		return i
	}
	i := uint32(g.codeBlock.AddIdentifier(id))
	g.identifierMap[id] = i
	return i
}

func (g *Generator) hasConstant(id ident.Identifier) bool {
	_, ok := g.identifierMap[id]
	return ok
}

// addConstantIndex reserves the next constant register.
func (g *Generator) addConstantIndex() int {
	index := g.nextConstantOffset
	g.constantPoolRegisters = append(g.constantPoolRegisters, newRegisterID(bytecode.VirtualRegisterForConstantIndex(index)))
	g.nextConstantOffset++
	return index
}

// addConstantEmptyValue returns the register holding JSValue() (the TDZ
// marker); it has its own cache because JSValue() cannot be hashed in JSC.
func (g *Generator) addConstantEmptyValue() *RegisterID {
	if g.emptyValueRegister == nil {
		index := g.addConstantIndex()
		g.codeBlock.AddConstant(bytecode.Empty{}, bytecode.SourceCodeRepresentationOther)
		g.emptyValueRegister = g.constantPoolRegisters[index]
	}
	return g.emptyValueRegister
}

// addConstantValue returns a constant register holding v, reusing an
// existing one for equal values.
func (g *Generator) addConstantValue(v bytecode.Constant, repr bytecode.SourceCodeRepresentation) *RegisterID {
	if _, ok := v.(bytecode.Empty); ok {
		return g.addConstantEmptyValue()
	}
	if n, ok := v.(bytecode.Number); ok && repr == bytecode.SourceCodeRepresentationDouble && n.Int32 {
		v = bytecode.JSDoubleNumber(n.Value)
	}
	if n, ok := v.(bytecode.Number); ok {
		// Numbers (most constants) have a map of their own: a key without an
		// interface hashes faster and is not allocated.
		key := numberConstantKey{math.Float64bits(n.Value), n.Int32, repr}
		if index, ok := g.numberConstants[key]; ok {
			return g.constantPoolRegisters[index]
		}
		index := g.addConstantIndex()
		g.numberConstants[key] = index
		g.codeBlock.AddConstant(v, repr)
		return g.constantPoolRegisters[index]
	}
	key := constantKey{bytecode.ConstantKey(v, repr), repr}
	if index, ok := g.jsValueMap[key]; ok {
		return g.constantPoolRegisters[index]
	}
	index := g.addConstantIndex()
	g.jsValueMap[key] = index
	g.codeBlock.AddConstant(v, repr)
	return g.constantPoolRegisters[index]
}

// moveLinkTimeConstant loads the link-time constant c into dst, or returns
// its constant register if dst is nil.
func (g *Generator) moveLinkTimeConstant(dst *RegisterID, c bytecode.LinkTimeConstant) *RegisterID {
	constant, ok := g.linkTimeConstantRegisters[c]
	if !ok {
		index := g.addConstantIndex()
		g.codeBlock.AddConstant(c, bytecode.SourceCodeRepresentationLinkTimeConstant)
		constant = g.constantPoolRegisters[index]
		g.linkTimeConstantRegisters[c] = constant
	}
	if dst == nil {
		return constant
	}
	return g.move(dst, constant)
}

func (g *Generator) moveEmptyValue(dst *RegisterID) *RegisterID {
	return g.move(dst, g.addConstantEmptyValue())
}

// emitLoad loads the constant v into dst, or returns its constant register
// if dst is nil.
func (g *Generator) emitLoad(dst *RegisterID, v bytecode.Constant) *RegisterID {
	return g.emitLoadWithRepresentation(dst, v, bytecode.SourceCodeRepresentationOther)
}

func (g *Generator) emitLoadWithRepresentation(dst *RegisterID, v bytecode.Constant, repr bytecode.SourceCodeRepresentation) *RegisterID {
	constantID := g.addConstantValue(v, repr)
	if dst != nil {
		return g.move(dst, constantID)
	}
	return constantID
}

func (g *Generator) emitLoadBool(dst *RegisterID, b bool) *RegisterID {
	return g.emitLoad(dst, bytecode.Bool(b))
}

// emitLoadIdentifier loads the string value of id.
func (g *Generator) emitLoadIdentifier(dst *RegisterID, id ident.Identifier) *RegisterID {
	if id.IsSymbol() {
		panic("bytecompiler: emitLoad of a symbol")
	}
	s, ok := g.stringMap[id]
	if !ok {
		s = bytecode.String(id.String())
		g.stringMap[id] = s
	}
	return g.emitLoad(dst, s)
}

// emitLoadIdentifierSet loads the index of an identifier set (used for the
// excluded keys of object rest patterns).
func (g *Generator) emitLoadIdentifierSet(dst *RegisterID, set []ident.Identifier) *RegisterID {
	setIndex := g.codeBlock.AddSetConstant(set)
	return g.emitLoad(dst, bytecode.JSNumber(float64(setIndex)))
}

func (g *Generator) emitLoadCompletionType(dst *RegisterID, t CompletionType) *RegisterID {
	return g.emitLoad(dst, bytecode.JSInt32(int32(t)))
}

// ResumeMode is JSGenerator::ResumeMode.
type ResumeMode int32

const (
	ResumeModeNormal ResumeMode = 0
	ResumeModeReturn ResumeMode = 1
	ResumeModeThrow  ResumeMode = 2
)

func (g *Generator) emitLoadResumeMode(dst *RegisterID, m ResumeMode) *RegisterID {
	return g.emitLoad(dst, bytecode.JSInt32(int32(m)))
}

// addBigIntConstant parses a BigInt literal and adds it to the pool.
func (g *Generator) addBigIntConstant(id ident.Identifier, radix uint8, sign bool) bytecode.BigInt {
	key := bigIntKey{id, radix, sign}
	if v, ok := g.bigIntMap[key]; ok {
		return v
	}
	n, ok := new(big.Int).SetString(id.String(), int(radix))
	if !ok {
		panic("bytecompiler: invalid BigInt literal " + id.String())
	}
	if sign {
		n.Neg(n)
	}
	v := bytecode.BigInt{Value: n}
	g.bigIntMap[key] = v
	g.addConstantValue(v, bytecode.SourceCodeRepresentationOther)
	return v
}

// addStringConstant adds the string value of id to the pool.
func (g *Generator) addStringConstant(id ident.Identifier) bytecode.String {
	if s, ok := g.stringMap[id]; ok {
		return s
	}
	s := bytecode.String(id.String())
	g.stringMap[id] = s
	g.addConstantValue(s, bytecode.SourceCodeRepresentationOther)
	return s
}

// addTemplateObjectConstant adds the template object for the tagged template
// ending at endOffset.
func (g *Generator) addTemplateObjectConstant(descriptor *bytecode.TemplateObjectDescriptor, endOffset int) *RegisterID {
	descriptorValue, ok := g.templateDescriptorMap[endOffset]
	if !ok {
		descriptorValue = &bytecode.TemplateObject{Descriptor: descriptor, EndOffset: endOffset}
		g.templateDescriptorMap[endOffset] = descriptorValue
	}
	index := g.addConstantIndex()
	g.codeBlock.AddConstant(descriptorValue, bytecode.SourceCodeRepresentationOther)
	return g.constantPoolRegisters[index]
}

// constantString returns the string value of a constant register.
func (g *Generator) constantString(r *RegisterID) (string, bool) {
	if !r.VirtualRegister().IsConstant() {
		return "", false
	}
	s, ok := g.codeBlock.Constant(r.VirtualRegister()).(bytecode.String)
	return string(s), ok
}
