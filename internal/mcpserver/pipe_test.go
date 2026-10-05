package mcpserver

import "io"

func newPipe() (*io.PipeReader, *io.PipeWriter) { return io.Pipe() }
