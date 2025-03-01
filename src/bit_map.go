// Copyright (c) 2022 XMM project Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// XMM Project Site: https://github.com/heiyeluren
// XMM URL: https://github.com/heiyeluren/XMM
//

package xmm

import (
	"errors"
	"fmt"
	"log"
	"reflect"
	"sync/atomic"
	"unsafe"
)

type gcBits uint32

func newMarkBits(nelems uintptr, zero bool) (*gcBits, error) {
	blocksNeeded := (nelems + 63) / 64
	uint32Needed := blocksNeeded * 2
	allocator := newXAllocator(4 * uint32Needed)
	p, err := allocator.alloc()
	if err != nil {
		return nil, err
	}
	bits := (*gcBits)(p)
	if zero {
		return bits, nil
	}
	for i := 0; i < int(uint32Needed); i++ {
		uint32p := bits.uint32p(uintptr(i))
		atomic.StoreUint32(uint32p, ^*uint32p)
	}
	return bits, nil
}

func newAllocBits(nelems uintptr) (*gcBits, error) {
	return newMarkBits(nelems, false)
}

// uint32p returns a pointer to the n'th byte of b.
func (b *gcBits) uint32p(n uintptr) *uint32 {
	return addb((*uint32)(b), n*4)
}

func (b *gcBits) invert(len uintptr) {
	num := len/32 + 1
	if len%32 == 0 {
		num -= 1
	}
	for i := 0; i < int(num); i++ {
		uint32p := b.uint32p(uintptr(i))
		atomic.StoreUint32(uint32p, ^*uint32p)
	}
}
func (b *gcBits) show32(len uintptr) {
	num := len/32 + 1
	if len%32 == 0 || len <= 32 {
		num -= 1
	}
	ss := ""
	for i := 0; i < int(num); i++ {
		uint32p := b.uint32p(uintptr(i))
		ss += fmt.Sprintf("%.32b ", *uint32p)
	}
	log.Println(ss)
}

func (b *gcBits) show64(len uintptr) {
	num := len/64 + 1
	if len%64 == 0 || len <= 64 {
		num -= 1
	}
	ss := ""
	for i := 0; i < int(num); i++ {
		bytes := (*[8]uint8)(unsafe.Pointer(uintptr(unsafe.Pointer(b)) + uintptr(8*i)))
		aCache := uint64(0)
		aCache |= uint64(bytes[0])
		aCache |= uint64(bytes[1]) << (1 * 8)
		aCache |= uint64(bytes[2]) << (2 * 8)
		aCache |= uint64(bytes[3]) << (3 * 8)
		aCache |= uint64(bytes[4]) << (4 * 8)
		aCache |= uint64(bytes[5]) << (5 * 8)
		aCache |= uint64(bytes[6]) << (6 * 8)
		aCache |= uint64(bytes[7]) << (7 * 8)
		ss += fmt.Sprintf("%.64b ", aCache)
	}
	log.Println(ss)
}

func String(b *gcBits, nelems uintptr) (res string) {
	sh := reflect.SliceHeader{
		Data: uintptr(unsafe.Pointer(b)),
		Len:  int(nelems),
		Cap:  int(nelems),
	}
	gcBitss := *(*[]gcBits)(unsafe.Pointer(&sh))
	for _, bitss := range gcBitss {
		item := fmt.Sprintf("%.32b", bitss)
		res += item
	}
	return res
}

func addb(p *uint32, n uintptr) *uint32 {
	// Note: wrote out full expression instead of calling add(p, n)
	// to reduce the number of temporaries generated by the
	// compiler for this trivial expression during inlining.
	return (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + n))
}

// bitp returns a pointer to the byte containing bit n and a mask for
// selecting that bit from *uint32p.
func (b *gcBits) bitp(n uintptr) (uint32p *uint32, mask uint32) {
	return b.uint32p(n / 32), 1 << (n % 32)
}

func markBitsForAddr(p uintptr, h *xHeap) error {
	s, err := h.spanOf(p)
	if err != nil {
		return err
	}
	objIndex := s.objIndex(p)
	s.setMarkBitsForIndex(objIndex)
	if logg {
		fmt.Println("markBitsForAddr", uintptr(unsafe.Pointer(s)), objIndex)
	}
	return nil
}

// isMarked reports whether mark bit m is set.
func (m markBits) isMarked() bool {
	return *m.uint32p&m.mask != 0
}

// setMarked sets the marked bit in the markbits, atomically.
func (m markBits) setMarked() {
	// Might be racing with other updates, so use atomic update always.
	// We used to be clever here and use a non-atomic update in certain
	// cases, but it's not worth the risk.
	//atomic.Or32(m.uint32p, m.mask)
	for {
		val := atomic.LoadUint32(m.uint32p)
		if atomic.CompareAndSwapUint32(m.uint32p, val, val^m.mask) {
			return
		}
	}
	//atomic.StoreUint32(m.uint32p, *m.uint32p^m.mask)
}

// setMarkedNonAtomic sets the marked bit in the markbits, non-atomically.
func (m markBits) setMarkedNonAtomic() {
	*m.uint32p |= m.mask
}

// clearMarked clears the marked bit in the markbits, atomically.
func (m markBits) clearMarked() {
	// Might be racing with other updates, so use atomic update always.
	// We used to be clever here and use a non-atomic update in certain
	// cases, but it's not worth the risk.
	//atomic.And32(m.uint32p, ^m.mask)
	for {
		val := atomic.LoadUint32(m.uint32p)
		if atomic.CompareAndSwapUint32(m.uint32p, val, val&(^m.mask)) {
			return
		}
	}
	//atomic.StoreUint32(m.uint32p, *m.uint32p&(^m.mask))
}

// markBitsForSpan returns the markBits for the span base address base.
func markBitsForSpan(base uintptr, h *xHeap) (mbits markBits, err error) {
	err = markBitsForAddr(base, h)
	if err != nil {
		return markBits{}, err
	}
	if mbits.mask != 1 {
		return mbits, errors.New("markBitsForSpan: unaligned start")
	}
	return mbits, nil
}

// advance advances the markBits to the next object in the span.
func (m *markBits) advance() {
	if m.mask == 1<<7 {
		m.uint32p = (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(m.uint32p)) + 1))
		m.mask = 1
	} else {
		m.mask = m.mask << 1
	}
	m.index++
}

type markBits struct {
	uint32p *uint32
	mask    uint32
	index   uintptr
}
