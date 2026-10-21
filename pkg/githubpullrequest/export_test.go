// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package githubpullrequest

// NewHTTPClientForTest exposes newHTTPClientWithBase for external tests.
// Mirrors githubreview/export_test.go.
var NewHTTPClientForTest = newHTTPClientWithBase
