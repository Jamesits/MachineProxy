// Package logging centralizes MachineProxy slog setup and the custom
// trace level. It receives configured log levels and writers from command
// entry points, and feeds the rest of the packages through slog's default
// logger and logging.LevelTrace.
package logging
