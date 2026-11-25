package devices

import (
	"github.com/santhosh-tekuri/jsonschema/v6"

	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
)

type ConfigInvalidError = jsonschemax.ConfigInvalidError

func compileSchema(src string) (*jsonschema.Schema, error) { return jsonschemax.Compile(src) }

func validateJSON(sch *jsonschema.Schema, raw []byte) (string, bool) {
	return jsonschemax.Validate(sch, raw)
}
