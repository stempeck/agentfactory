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
		{"NODE_APT_VERSION", "22.23.4-1nodesource1"},
		{"GH_VERSION", "2.103.2"},
		{"GH_SHA256_ARM64", "4c3e99a50bd8b181a3fe9d69b25dd978bac768148d88f01507c6b4d1ef61a443"},
		{"GH_SHA256_AMD64", "62ccf747115ddc61237efffdde66a3c8f70ba249433dc64f70801089b59c59c8"},
		{"GO_TOOLCHAIN_VERSION", "go1.26.10"},
		{"GO_SHA256_ARM64", "3f4227ce7b206a513e794f1393adb473a07b46e94d18bd15d1c684bbb7319797"},
		{"GO_SHA256_AMD64", "ccfe7ae822d2c2f41f4fe7c956199967a4dac22d0ee0d2b2bfdb440aab3b20c5"},
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
		{"CLAUDE_CODE_VERSION", "2.1.308"},
		{"CLAUDE_CODE_SHA256_ARM64", "fdd22e6cbb734d580f5664c48df4294eeec850218ff07f186f9af30426396ee1"},
		{"CLAUDE_CODE_SHA256_AMD64", "356733f185fa988e6bf3aa9a555269fdd167982a2e2a80d3c8a78037bb92ad58"},
		{"CLAUDE_CODE_NPM_SHA512", "6f61e7b3383237bdf045314ff8860f0c9367f400d79c6ea3af6ea26cbfb0d371d22aa7dbe86a84f099dfaf89b3dfb66fe004ff34ff81e7fddcd45f3d59375648"},
		{"CLAUDE_CODE_LINUX_ARM64_SHA512", "c6d4f609dee7fb264df4f8966d61da65ff70db961f955fbb54e5f92013c1fc8584aca64586cb2ddbf032d6d4a0abf333a75be9e7ac70151a8f6ba50023d05923"},
		{"CLAUDE_CODE_LINUX_AMD64_SHA512", "7df52bc9ebe91fb3ce4ad3c9ff1a54239475a3361b1fda3f0db87fc4f4a6c9525e8442e744c6664677a37509aad137a3396fd26cd9c9161086320c08b0ea854d"},
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
