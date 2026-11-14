package cmd

import (
	"regexp"
	"testing"
)

// TestQuickstartPinValues is the one owner of every K6 constant's absolute value (D-61): every other
// test reads a pin through quickstartPin. LITELLM_VERSION stays with TestQuickstartLitellmPinValue.
// The anchored, quoted form also rejects an unquoted literal and a ${X:-…} default (D-29: the image
// exports GO_SHA256_*).
func TestQuickstartPinValues(t *testing.T) {
	pins := []struct{ name, value string }{
		{"NODE_MAJOR", "22"},
		{"NODE_APT_VERSION", "22.23.5-1nodesource1"},
		{"GH_VERSION", "2.103.2"},
		{"GH_SHA256_ARM64", "4c3e99a50bd8b181a3fe9d69b25dd978bac768148d88f01507c6b4d1ef61a443"},
		{"GH_SHA256_AMD64", "62ccf747115ddc61237efffdde66a3c8f70ba249433dc64f70801089b59c59c8"},
		{"GO_TOOLCHAIN_VERSION", "go1.27.4"},
		{"GO_SHA256_ARM64", "3392582988a273a87ab5043ee1b2c9c17a2c3c00580bf9326c47a2e0aaaeac92"},
		{"GO_SHA256_AMD64", "7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840"},
		{"PLAYWRIGHT_VERSION", "1.64.1"},
		{"PLAYWRIGHT_SHA512", "1c2ad5847af9410bb0628b301141936fb806a79db6c9f95bbff493f27178b05586b8b8d00add7b43f12e6e918648d2989f5b6c4c23d9c6a1e11c27d5511bbb50"},
		{"PLAYWRIGHT_CORE_SHA512", "042e2cf32e9211503a435ad2c594eda3c848d4a508df15e51c6660bc6a8fe08a8f748fdae855fd1f6f480cdcfbbcaa3d976d5be8b7d8a2c49c3cbe8cdf1c720d"},
		{"PLAYWRIGHT_CHROMIUM_TREE_SHA256_ARM64", "ba6d2c17e743830e728e04eddbbd329ffaa69560d62621546086a221ca23ea21"},
		{"PLAYWRIGHT_CHROMIUM_TREE_SHA256_AMD64", "221c5699c30db0c6eebe41ea883632001e2fb113a6dcdedabfce1dd5aa35d3dd"},
		{"PLAYWRIGHT_HEADLESS_SHELL_TREE_SHA256_ARM64", "875c4b701f9d9d2f667385654ffd9d4dfa67c46d9542121eba80a9a556b1cc39"},
		{"PLAYWRIGHT_HEADLESS_SHELL_TREE_SHA256_AMD64", "a292efc9ef3eb31933f20151cc39e619ba5b8d8caccd4e62c0dbfc6c1e41e4f3"},
		{"CODEX_VERSION", "0.164.0"},
		{"CODEX_SHA512", "4d68fb506c8f45edcf8ff03775d340c6cd6d7b76bf5bfc4077e2742c7aeb1539ac49f6a118c38b17fbe7f3c0f7402220013dc1d6b9b5cbf677358134af1e9c65"},
		{"CODEX_LINUX_ARM64_SHA512", "2cbe624d08f20cbe18457e38116aa121f110e6e1a79a0c64401252c63652789f0bd4e83cde9765cd4256fcf449efaa1a570395821e875f0b8f12204d10753896"},
		{"CODEX_LINUX_AMD64_SHA512", "5cb028696b06393da487397e3f81580cf4e4cc0afd5b45455c569592a945764334b286b5807ff84b4c120f9ec49371c7c248ab0041b3dbf3307a1b1e87e97932"},
		{"CLAUDE_CODE_VERSION", "2.1.322"},
		{"CLAUDE_CODE_SHA256_ARM64", "f220ca50ea4f3222043ff83cc224a1262e0196e9897acc0871efa94235b0c1fd"},
		{"CLAUDE_CODE_SHA256_AMD64", "bfb3703ad5380242328459bc62ada4bde8ae7ba57c6e1eeadb9c00e08874c478"},
		{"CLAUDE_CODE_NPM_SHA512", "a0fee85b8bcce4344459f2f58e7e094869662dcac9e509a9a60003ad53490543e20b5990e58bd1ae47b4a27f9867fdd084a21a089b5dafa8cfbde85c0102072d"},
		{"CLAUDE_CODE_LINUX_ARM64_SHA512", "618369d68a7ced659c04413a8c2d6778e8cc0cf767e08cbe9141f215c2351ac2e5e08c07ec48d997054a729b19a956c4d085c415a3a5766c8e1c2d72de3d7c02"},
		{"CLAUDE_CODE_LINUX_AMD64_SHA512", "d03a1a3405cb58261820669c85604291352c4bc74e3a0be4e11d9f14a76b2de8cef579514f069650c0fd38d7439afd1cb1da70c63e2a2d437acd631a1b64b3ea"},
		{"EMSIGN_G3_BODY_PROBE", "FBVIcUqEqy6HyC5ltqtdyzdc6LBtCGI79G1Y4PPwT01xyS"},
		{"OPENOBSERVE_VERSION", "v1.1.2"},
		{"OPENOBSERVE_SHA256_AMD64", "225883447a9e3c75cb52d5a64ece8618cf0d8f3d2eb0b424eebb2e51b0126faa"},
		{"OPENOBSERVE_SHA256_ARM64", "4d00e03155594dba473202fdca4f2b8c3b549138f9bab4ff0f6894feda4b7354"},
	}
	if len(pins) != 29 {
		t.Fatalf("AC 1 names 29 K6 constants; this table has %d", len(pins))
	}

	content := readQuickstart(t)
	for _, p := range pins {
		want := p.name + `="` + p.value + `"`
		re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(want) + `$`)
		if n := len(re.FindAllStringIndex(content, -1)); n != 1 {
			t.Errorf("quickstart.sh must declare %s exactly once as a line of its own; found %d — a version change "+
				"must be a deliberate, reviewed edit", want, n)
		}
		anyAssign := regexp.MustCompile(`(?m)^` + p.name + `=`)
		if n := len(anyAssign.FindAllStringIndex(content, -1)); n > 1 {
			t.Errorf("quickstart.sh assigns %s %d times at column 0; a second assignment would shadow the pin", p.name, n)
		}
	}
}
