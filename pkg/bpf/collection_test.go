// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package bpf

import (
	"encoding/binary"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/btf"
	"github.com/stretchr/testify/assert"

	"github.com/cilium/cilium/pkg/testutils"
)

// Generate a program of sufficient size whose verifier log does not fit into a
// 128-byte buffer. Load the program while requesting a verifier log using an
// undersized buffer and expect the load to be successful.
func TestLoadCollectionResizeLogBuffer(t *testing.T) {
	testutils.PrivilegedTest(t)

	num := 32
	insns := make(asm.Instructions, 0, num)
	for i := 0; i < num-1; i++ {
		insns = append(insns, asm.Mov.Reg(asm.R0, asm.R1))
	}
	insns = append(insns, asm.Return())

	spec := &ebpf.CollectionSpec{
		Programs: map[string]*ebpf.ProgramSpec{
			"test": {
				Type:         ebpf.SocketFilter,
				License:      "MIT",
				Instructions: insns,
			},
		},
	}

	coll, err := LoadCollection(spec, ebpf.CollectionOptions{
		Programs: ebpf.ProgramOptions{
			// Request instruction-level verifier state to ensure sufficient
			// output is generated by the verifier. For example, one instruction:
			// 0: (bf) r0 = r1		; R0_w=ctx(off=0,imm=0) R1=ctx(off=0,imm=0)
			LogLevel: ebpf.LogLevelInstruction,
			// Set the minimum buffer size the kernel will accept. LoadCollection is
			// expected to grow this sufficiently over multiple tries.
			LogSize: 128,
		},
	})
	if err != nil {
		t.Fatal("Error loading collection:", err)
	}

	// Expect successful program creation, with a complementary verifier log.
	log := coll.Programs["test"].VerifierLog
	if len(log) == 0 {
		t.Fatal("Received empty verifier log")
	}
}

func TestInlineGlobalData(t *testing.T) {
	spec := &ebpf.CollectionSpec{
		ByteOrder: binary.LittleEndian,
		Maps: map[string]*ebpf.MapSpec{
			globalDataMap: {
				Value: &btf.Datasec{
					Vars: []btf.VarSecinfo{
						{Offset: 0, Size: 4, Type: &btf.Var{}},
						{Offset: 4, Size: 2, Type: &btf.Var{}},
						{Offset: 8, Size: 8, Type: &btf.Var{}},
					},
				},
				Contents: []ebpf.MapKV{{Value: []byte{
					// var 1
					0x0, 0x0, 0x0, 0x80,
					// var 2 has padding since var 3 aligns to 64 bits. Fill the padding
					// with garbage to test if it gets masked correctly by the inliner.
					0x1, 0x0, 0xff, 0xff,
					// var 3
					0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x7f,
				}}},
			},
		},
		Programs: map[string]*ebpf.ProgramSpec{
			"prog1": {
				Instructions: asm.Instructions{
					// Pseudo-load at offset 0. This Instruction would have func_info when
					// read from an ELF, so validate Metadata preservation after inlining
					// global data.
					asm.LoadMapValue(asm.R0, 0, 0).WithReference(globalDataMap).WithSymbol("func1"),
					// Pseudo-load at offset 4.
					asm.LoadMapValue(asm.R0, 0, 4).WithReference(globalDataMap),
					// Pseudo-load at offset 8, pointing at a u64.
					asm.LoadMapValue(asm.R0, 0, 8).WithReference(globalDataMap),
					asm.Return(),
				},
			},
		},
	}

	if err := inlineGlobalData(spec); err != nil {
		t.Fatal(err)
	}

	insns := spec.Programs["prog1"].Instructions
	if want, got := 0x80000000, int(insns[0].Constant); want != got {
		t.Errorf("unexpected Instruction constant: want: 0x%x, got: 0x%x", want, got)
	}

	if want, got := "func1", insns[0].Symbol(); want != got {
		t.Errorf("unexpected Symbol value of Instruction: want: %s, got: %s", want, got)
	}

	if want, got := 0x1, int(insns[1].Constant); want != got {
		t.Errorf("unexpected Instruction constant: want: 0x%x, got: 0x%x", want, got)
	}

	if want, got := 0x7f00000000000000, int(insns[2].Constant); want != got {
		t.Errorf("unexpected Instruction constant: want: 0x%x, got: 0x%x", want, got)
	}
}

func TestRemoveUnreachableTailcalls(t *testing.T) {
	spec, err := ebpf.LoadCollectionSpec("testdata/unreachable-tailcall.o")
	if err != nil {
		t.Fatal(err)
	}

	assert.Contains(t, spec.Programs, "cil_entry")
	assert.Contains(t, spec.Programs, "a")
	assert.Contains(t, spec.Programs, "b")
	assert.Contains(t, spec.Programs, "c")
	assert.Contains(t, spec.Programs, "d")
	assert.Contains(t, spec.Programs, "e")

	if err := removeUnreachableTailcalls(spec); err != nil {
		t.Fatal(err)
	}

	assert.Contains(t, spec.Programs, "cil_entry")
	assert.Contains(t, spec.Programs, "a")
	assert.Contains(t, spec.Programs, "b")
	assert.Contains(t, spec.Programs, "c")
	assert.NotContains(t, spec.Programs, "d")
	assert.NotContains(t, spec.Programs, "e")
}
