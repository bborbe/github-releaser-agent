// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package githubchangelog

// NewHTTPFetcherForTest constructs a fetcher pointed at a custom API base
// URL. Exported only via export_test.go so tests can substitute httptest
// server URLs without exposing the seam in the production API.
var NewHTTPFetcherForTest = newHTTPFetcherWithBase
