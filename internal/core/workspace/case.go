package workspace

import "runtime"

var caseInsensitiveFS = runtime.GOOS == "windows" || runtime.GOOS == "darwin"
