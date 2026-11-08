// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package imageregistry

// NewClientForTest exposes newClientWithBase for external tests. Mirrors
// githubreview/export_test.go.
var NewClientForTest = newClientWithBase

// RegistryAPIHostForTest exposes the unexported registryAPIHost mapping so
// the external test package can assert it directly — newClientWithBase
// bypasses the mapping, so the production path needs its own coverage.
var RegistryAPIHostForTest = registryAPIHost
