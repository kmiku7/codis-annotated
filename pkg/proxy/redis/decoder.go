// Copyright 2014 Wandoujia Inc. All Rights Reserved.
// Licensed under the MIT (MIT-LICENSE.txt) license.

package redis

import (
	"bufio"
	"bytes"
	"io"
	"strconv"

	"github.com/wandoulabs/codis/pkg/utils/errors"
)

var (
	ErrBadRespCRLFEnd  = errors.New("bad resp CRLF end")
	ErrBadRespBytesLen = errors.New("bad resp bytes len")
	ErrBadRespArrayLen = errors.New("bad resp array len")
)

type Decoder struct {
	*bufio.Reader

	Err error
}

func NewDecoder(br *bufio.Reader) *Decoder {
	return &Decoder{Reader: br}
}

// 这是bufio的size
func NewDecoderSize(r io.Reader, size int) *Decoder {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, size)
	}
	return &Decoder{Reader: br}
}

func (d *Decoder) Decode() (*Resp, error) {
	if d.Err != nil {
		return nil, d.Err
	}
	// 0 指的解析层次
	r, err := d.decodeResp(0)
	if err != nil {
		d.Err = err
	}
	return r, err
}

func Decode(br *bufio.Reader) (*Resp, error) {
	return NewDecoder(br).Decode()
}

func DecodeFromBytes(p []byte) (*Resp, error) {
	return Decode(bufio.NewReader(bytes.NewReader(p)))
}

func (d *Decoder) decodeResp(depth int) (*Resp, error) {
	b, err := d.ReadByte()
	if err != nil {
		return nil, errors.Trace(err)
	}
	switch t := RespType(b); t {
	// 这些的格式都是'Type-Char' + Contents + \r\n
	case TypeString, TypeError, TypeInt:
		r := &Resp{Type: t}
		r.Value, err = d.decodeTextBytes()
		return r, err
	// 字符串类型
	case TypeBulkBytes:
		r := &Resp{Type: t}
		r.Value, err = d.decodeBulkBytes()
		return r, err
	case TypeArray:
		r := &Resp{Type: t}
		r.Array, err = d.decodeArray(depth)
		return r, err
	default:
		if depth != 0 {
			return nil, errors.Errorf("bad resp type %s", t)
		}
		if err := d.UnreadByte(); err != nil {
			return nil, errors.Trace(err)
		}
		// 简单协议, 映射到TypeArray
		r := &Resp{Type: TypeArray}
		r.Array, err = d.decodeSingleLineBulkBytesArray()
		return r, err
	}
}

// 一直读到\r\n
func (d *Decoder) decodeTextBytes() ([]byte, error) {
	// 包含分隔符
	b, err := d.ReadBytes('\n')
	if err != nil {
		return nil, errors.Trace(err)
	}
	if n := len(b) - 2; n < 0 || b[n] != '\r' {
		return nil, errors.Trace(ErrBadRespCRLFEnd)
	} else {
		return b[:n], nil
	}
}

func (d *Decoder) decodeTextString() (string, error) {
	b, err := d.decodeTextBytes()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (d *Decoder) decodeInt() (int64, error) {
	s, err := d.decodeTextString()
	if err != nil {
		return 0, err
	}
	if n, err := strconv.ParseInt(s, 10, 64); err != nil {
		return 0, errors.Trace(err)
	} else {
		return n, nil
	}
}

func (d *Decoder) decodeBulkBytes() ([]byte, error) {
	n, err := d.decodeInt()
	if err != nil {
		return nil, err
	}
	if n < -1 {
		return nil, errors.Trace(ErrBadRespBytesLen)
	} else if n == -1 {
		return nil, nil
	}
	// 确保规则合法, content长度一定是n, 且后跟\r\n
	b := make([]byte, n+2)
	if _, err := io.ReadFull(d.Reader, b); err != nil {
		return nil, errors.Trace(err)
	}
	if b[n] != '\r' || b[n+1] != '\n' {
		return nil, errors.Trace(ErrBadRespCRLFEnd)
	}
	return b[:n], nil
}

func (d *Decoder) decodeArray(depth int) ([]*Resp, error) {
	n, err := d.decodeInt()
	if err != nil {
		return nil, err
	}
	if n < -1 {
		return nil, errors.Trace(ErrBadRespArrayLen)
	} else if n == -1 {
		return nil, nil
	}
	a := make([]*Resp, n)
	// 递归处理, 支持各种类型
	for i := 0; i < len(a); i++ {
		if a[i], err = d.decodeResp(depth + 1); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// 解析旧的通信协议(?)
// 简单协议
// 单行, 以\r\n结尾, 元素间以空格分割
func (d *Decoder) decodeSingleLineBulkBytesArray() ([]*Resp, error) {
	b, err := d.decodeTextBytes()
	if err != nil {
		return nil, err
	}
	a := make([]*Resp, 0, 4)
	for l, r := 0, 0; r <= len(b); r++ {
		if r == len(b) || b[r] == ' ' {
			if l < r {
				a = append(a, &Resp{
					Type:  TypeBulkBytes,
					Value: b[l:r],
				})
			}
			l = r + 1
		}
	}
	return a, nil
}
