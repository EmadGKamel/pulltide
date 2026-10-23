package execruntime

import (
	"context"
	"io"
)

func copyContext(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	var total int64
	buffer := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			return total, ErrRuntime
		}
		n, e := r.Read(buffer)
		if n > 0 {
			written, err := w.Write(buffer[:n])
			total += int64(written)
			if err != nil {
				return total, err
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if e == io.EOF {
			return total, nil
		}
		if e != nil {
			return total, e
		}
	}
}
