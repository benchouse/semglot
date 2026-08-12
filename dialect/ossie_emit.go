package dialect

import (
	"fmt"

	"github.com/benchouse/semglot/ir"
)

// WithOptions returns an ossie emitter carrying the model identity and the
// database/schema used to qualify each dataset's `source`.
func (ossie) WithOptions(o Options) Emitter {
	return ossie{Database: o.Database, Schema: o.Schema, ModelName: o.Name, Description: o.Description}
}

// Emit is implemented in a later task; ossie registers as an Emitter now so the
// registry wiring can be tested.
func (o ossie) Emit(m *ir.Model, dir string) ([]string, error) {
	return nil, fmt.Errorf("ossie: emit not yet implemented")
}
