package utils

import "io"

// the idea of the Closer interface is to force the user
// of functions that returns objects which needs cleanup to provide an input for registering the cleanup function
// E.g.:
// func OpenFile(name string, closer Closer) (*File, error) {
// 	file, err := os.Open(name)
// 	if err != nil {
// 		return nil, err
// 	}
// 	closer.RegisterClosable(file)
// 	return file, nil
// }
// This way the user of the function is forced to think about cleanup

type Closer interface {
	io.Closer
	RegisterCleanupFunc(closable func() error)
	RegisterCloseable(closable io.Closer)
	UnregisterCloseable(closable io.Closer)
	UnregisterAll()
}

type closer struct {
	registeredCloseables []io.Closer
}

type closingFunc struct {
	closable func() error
}

func (c closingFunc) Close() error {
	return c.closable()
}

func NewCloser() Closer {
	return &closer{
		registeredCloseables: []io.Closer{},
	}
}

func (c *closer) Close() error {
	// close in inverse order of registration
	err := error(nil)
	for i := len(c.registeredCloseables) - 1; i >= 0; i-- {
		currErr := c.registeredCloseables[i].Close()
		if err == nil {
			err = currErr
		}
	}
	c.registeredCloseables = []io.Closer{}
	return err
}

func (c *closer) RegisterCleanupFunc(closeable func() error) {
	c.registeredCloseables = append(c.registeredCloseables, closingFunc{closeable})
}

func (c *closer) RegisterCloseable(closeable io.Closer) {
	c.registeredCloseables = append(c.registeredCloseables, closeable)
}

func (cr *closer) UnregisterCloseable(closeable io.Closer) {
	for i, c := range cr.registeredCloseables {
		if c == closeable {
			cr.registeredCloseables = append(cr.registeredCloseables[:i], cr.registeredCloseables[i+1:]...)
		}
	}
}

func (cr *closer) UnregisterAll() {
	cr.registeredCloseables = []io.Closer{}
}
