package sqlschema

import (
	"log/slog"
	reflect "reflect"
)

type Option func(any)

func WithLogger(logger *slog.Logger, component string) Option {
	return func(t any) {
		if logger == nil {
			logger = slog.Default()
		}

		// Use reflection to set the Logger field
		v := reflect.ValueOf(t).Elem()
		if loggerField := v.FieldByName("Logger"); loggerField.IsValid() && loggerField.CanSet() {
			logger = logger.With(slog.String("component", component))
			loggerField.Set(reflect.ValueOf(logger))
		} else {
			slog.Error("logger not found")
		}
	}
}
