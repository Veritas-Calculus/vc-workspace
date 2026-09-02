package main

import (
	"bytes"
	"encoding/binary"
	"os"
)

type iconElement struct {
	kind string
	data []byte
}

func main() {
	if len(os.Args) < 4 || len(os.Args)%2 != 0 {
		panic("usage: iconpack output.icns type image.png [type image.png ...]")
	}
	elements := make([]iconElement, 0, (len(os.Args)-2)/2)
	total := uint32(8)
	for index := 2; index < len(os.Args); index += 2 {
		kind := os.Args[index]
		if len(kind) != 4 {
			panic("icon element type must contain four bytes")
		}
		data, err := os.ReadFile(os.Args[index+1])
		if err != nil {
			panic(err)
		}
		elements = append(elements, iconElement{kind: kind, data: data})
		total += uint32(8 + len(data))
	}

	var output bytes.Buffer
	output.WriteString("icns")
	_ = binary.Write(&output, binary.BigEndian, total)
	for _, element := range elements {
		output.WriteString(element.kind)
		_ = binary.Write(&output, binary.BigEndian, uint32(8+len(element.data)))
		output.Write(element.data)
	}
	if err := os.WriteFile(os.Args[1], output.Bytes(), 0o644); err != nil {
		panic(err)
	}
}
