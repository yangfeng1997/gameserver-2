// Package app provides the process framework for project services.
//
// The framework is intentionally small:
//   - App is a concrete process orchestrator, not a base class.
//   - Modules are explicit lifecycle units.
//   - Start-up order follows declared dependencies.
//   - Shutdown is reverse-order and context-driven.
//   - Optional Post dispatch exists for single-threaded serialization needs.
package app
