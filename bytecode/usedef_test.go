package bytecode

import "testing"

// Every opcode the generator can emit must have use information (the C++
// switch asserts on unknown opcodes).
func TestUseDefTablesComplete(t *testing.T) {
	special := map[OpcodeID]bool{
		OpIteratorOpenID: true, OpAsyncIteratorOpenID: true, OpIteratorNextID: true,
		OpAsyncIteratorNextID: true, OpNewArrayWithSpreadID: true, OpNewArrayID: true,
		OpStrcatID: true, OpConstructID: true, OpSuperConstructID: true, OpCallDirectEvalID: true,
		OpCallID: true, OpTailCallID: true, OpCallIgnoreResultID: true, OpInstanceofID: true,
		OpWide16ID: true, OpWide32ID: true, // never emitted: all operands are 32 bits
	}
	for op := range OpcodeID(NumberOfBytecodeIDs) {
		if simpleUses[op] == nil && !special[op] {
			t.Errorf("no use information for %s", op)
		}
	}
}
