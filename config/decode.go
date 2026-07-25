package config

import (
	"context"

	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

func Decode(ctx context.Context, content []byte) (option.Options, Dialect, error) {
	dialect, err := Detect(content)
	if err != nil {
		return option.Options{}, "", err
	}
	if dialect == DialectXray {
		options, translateErr := translateXray(ctx, content)
		if translateErr != nil {
			return option.Options{}, dialect, E.Cause(translateErr, "translate Xray config")
		}
		return options, dialect, nil
	}
	options, err := json.UnmarshalExtendedContext[option.Options](ctx, content)
	if err != nil {
		return option.Options{}, dialect, err
	}
	return options, dialect, nil
}
