package logger

import (
	"encoding/json"
	"math/big"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
)

type vandalBasicBlock struct {
	Entry   uint64
	Exit    uint64
	Ops     []*vandalLogMarshalling
	Address common.Address
}

type vandalLog struct {
	Pc    uint64
	Op    vm.OpCode
	Gas   uint64
	Cost  uint64
	Ret   []byte
	Value *big.Int
}

type vandalLogMarshalling struct {
	Pc        uint64
	Op        vm.OpCode
	Gas       uint64
	Cost      uint64
	Depth     int
	CallIndex int
	Ret       []byte
	Value     *big.Int
	Block     *vandalBasicBlock `json:"-"`
}

type VandalLogger struct {
	env *vm.EVM

	logs      []vandalLog
	reason    error
	interrupt atomic.Bool

	CallStack []vandalLog
}

func (bb *vandalBasicBlock) Split(entry uint64) vandalBasicBlock {
	new := vandalBasicBlock{entry, bb.Exit, make([]*vandalLogMarshalling, 0), bb.Address}
	bb.Exit = entry - 1
	bb.Ops = bb.Ops[:entry-bb.Entry]
	new.Ops = bb.Ops[entry-bb.Entry:]

	for _, op := range new.Ops {
		op.Block = &new
	}

	for _, op := range bb.Ops {
		op.Block = bb
	}

	return new
}

func NewVandalTracer() *VandalLogger {
	return &VandalLogger{}
}

// CaptureStart implements the EVMLogger interface to initialize the tracing operation.
func (l *VandalLogger) CaptureStart(env *vm.EVM, from common.Address, to common.Address, create bool, input []byte, gas uint64, value *big.Int) {
	l.env = env
	l.CallStack = make([]vandalLog, 0)
}

// CaptureState implements the EVMLogger interface to trace a single step of VM execution.
func (l *VandalLogger) CaptureState(pc uint64, op vm.OpCode, gas, cost uint64, res []byte) {
	if l.interrupt.Load() {
		return
	}

	log := vandalLog{
		Pc:   pc,
		Op:   op,
		Gas:  gas,
		Cost: cost,
		Ret:  res,
	}

	l.logs = append(l.logs, log)
}

// CaptureEnter is called when EVM enters a new scope (via call, create or selfdestruct).
func (l *VandalLogger) CaptureEnter(op vm.OpCode, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
}

// CaptureExit is called when EVM exits a scope, even if the scope didn't
// execute any code.
func (l *VandalLogger) CaptureExit(output []byte, gasUsed uint64, err error) {
}

// CaptureFault implements the EVMLogger interface to trace an execution fault.
func (l *VandalLogger) CaptureFault(pc uint64, op vm.OpCode, gas, cost uint64, scope *vm.ScopeContext, depth int, err error) {
}

// CaptureEnd is called after the call finishes to finalize the tracing.
func (l *VandalLogger) CaptureEnd(output []byte, gasUsed uint64, err error) {}

func (l *VandalLogger) CaptureTxStart(gasLimit uint64) {
}

func (l *VandalLogger) CaptureTxEnd(restGas uint64) {}

// GetResult returns the json-encoded nested list of call traces, and any
// error arising from the encoding or forceful termination (via `Stop`).
func (l *VandalLogger) GetResult() (json.RawMessage, error) {
	if l.reason != nil {
		return nil, l.reason
	}

	blocks := make([]vandalBasicBlock, 0)
	entry := uint64(0)
	exit := uint64(len(l.logs) - 1)
	callIndex := uint64(0)
	depth := 0
	current := vandalBasicBlock{entry, exit, make([]*vandalLogMarshalling, 0), common.Address{}}
	marshalLogs := make([]*vandalLogMarshalling, 0, len(l.logs))

	for i, log := range l.logs {
		if log.Pc == 0 && i != 0 {
			callIndex++
		}
		marshalLogs = append(marshalLogs, &vandalLogMarshalling{
			Pc:        log.Pc,
			Op:        log.Op,
			Gas:       log.Gas,
			Cost:      log.Cost,
			Depth:     depth,
			CallIndex: int(callIndex),
			Ret:       log.Ret,
			Value:     log.Value,
		})
	}

	for i, log := range marshalLogs {
		log.Block = &current
		current.Ops = append(current.Ops, log)

		if log.Pc == 0 && i == 0 {
			depth = 1
		} else if log.Pc == 0 {
			depth--
			new := current.Split(uint64(i))
			blocks = append(blocks, current)
			current = new
		} else if GetKind(log.Op) == OpKindOne || GetKind(log.Op) == OpKindFive {
			if !(marshalLogs[i-1].CallIndex == log.CallIndex &&
				log.Pc-marshalLogs[i-1].Pc == uint64(pcGap(marshalLogs[i-1].Op)) &&
				!possiblyHalts(marshalLogs[i-1].Op)) {

				depth -= 1
				new := current.Split(uint64(i))
				blocks = append(blocks, current)
				current = new
			}
		} else if i == len(marshalLogs)-1 {
			blocks = append(blocks, current)
		}

		log.Depth = depth
		log.CallIndex = int(callIndex)
	}

	return json.Marshal(blocks)
}

// Stop terminates execution of the tracer at the first opportune moment.
func (l *VandalLogger) Stop(err error) {
	l.reason = err
	l.interrupt.Store(true)
}

type OpKind int

const (
	OpKindUnknown       OpKind = 0
	OpKindOne           OpKind = 1
	OpKindTwo           OpKind = 2
	OpKindThreeLoad     OpKind = 3
	OpKindThreeStoreOne OpKind = 4
	OpKindThreeStoreTwo OpKind = 5
	OpKindFour          OpKind = 6
	OpKindFive          OpKind = 7
)

func possiblyHalts(op vm.OpCode) bool {
	switch op.String() {
	case vm.STOP.String(),
		vm.REVERT.String(),
		vm.SELFDESTRUCT.String(),
		vm.RETURN.String():
		return true

	default:
		return false
	}
}

func pcGap(op vm.OpCode) int {
	if op.IsPush() {
		return int(op - vm.PUSH1 + 1)
	} else {
		return 1
	}
}

func GetKind(op vm.OpCode) OpKind {
	switch op.String() {
	case
		vm.ADDRESS.String(),
		vm.ORIGIN.String(),
		vm.CALLER.String(),
		vm.CALLVALUE.String(),
		vm.CALLDATASIZE.String(),
		vm.CODESIZE.String(),
		vm.GASPRICE.String(),
		vm.RETURNDATASIZE.String(),
		vm.COINBASE.String(),
		vm.TIMESTAMP.String(),
		vm.NUMBER.String(),
		vm.DIFFICULTY.String(),
		vm.GASLIMIT.String(),
		vm.PC.String(),
		vm.MSIZE.String(),
		vm.GAS.String():

		return OpKindOne
	case
		vm.KECCAK256.String(),
		vm.BALANCE.String(),
		vm.CALLDATALOAD.String(),
		vm.EXTCODESIZE.String(),
		vm.BLOCKHASH.String():

		return OpKindTwo
	case
		vm.SLOAD.String():

		return OpKindThreeLoad
	case
		vm.SSTORE.String(),
		vm.MSTORE.String(),
		vm.MSTORE8.String():

		return OpKindThreeStoreOne
	case
		vm.CALLDATACOPY.String(),
		vm.CODECOPY.String(),
		vm.EXTCODECOPY.String(),
		vm.RETURNDATACOPY.String():

		return OpKindThreeStoreTwo
	case
		vm.CALL.String(),
		vm.CALLCODE.String(),
		vm.DELEGATECALL.String(),
		vm.STATICCALL.String():

		return OpKindFour

	case
		vm.CREATE.String(),
		vm.CREATE2.String():

		return OpKindFive

	default:
		return OpKindUnknown
	}
}
