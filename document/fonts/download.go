package fonts

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/h0rn3t/docx2pdf/internal/cab"
)

// maxDownload caps a downloaded file or archive member: the largest font in
// the catalog is under 1 MiB and the DejaVu archive under 6 MiB.
const maxDownload = 32 << 20

// A remoteFile is one font file of the download catalog.
type remoteFile struct {
	name   string // file name in the font directory
	family string // the family its name table declares
	url    string // the TTF itself, or the archive holding it
	member string // path inside the archive; empty when url is the TTF
	sha256 string // of the TTF
}

// Upstream sources, each pinned to one commit or release so a checksum
// never goes stale.
const (
	googleFonts = "https://raw.githubusercontent.com/google/fonts/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/"
	gelasio     = "https://raw.githubusercontent.com/SorkinType/Gelasio/7ab20e7e5c42791e603b9ee3201a0b49849cfdb2/fonts/ttf/"
	dejaVu      = "https://github.com/dejavu-fonts/dejavu-fonts/releases/download/version_2_37/dejavu-fonts-ttf-2.37.zip"
	dejaVuDir   = "dejavu-fonts-ttf-2.37/ttf/"
	// corefonts mirrors the installers Microsoft published until 2002, as
	// winetricks fetches them; Tahoma comes from an Internet Explorer 5.5
	// language pack, mirrored by the corefonts project.
	corefonts = "https://github.com/pushcx/corefonts/raw/master/"
	tahoma    = "https://downloads.sourceforge.net/corefonts/OldFiles/IELPKTH.CAB"
)

// archiveSums are the checksums of the archives fonts are extracted from -
// for Microsoft's installers, the ones winetricks pins.
var archiveSums = map[string]string{
	dejaVu:                     "7576310b219e04159d35ff61dd4a4ec4cdba4f35c00e002a136f00e96a908b0a",
	corefonts + "andale32.exe": "0524fe42951adc3a7eb870e32f0920313c71f170c859b5f770d82b4ee111e970",
	corefonts + "arial32.exe":  "85297a4d146e9c87ac6f74822734bdee5f4b2a722d7eaa584b7f2cbf76f478f6",
	corefonts + "arialb32.exe": "a425f0ffb6a1a5ede5b979ed6177f4f4f4fdef6ae7c302a7b7720ef332fec0a8",
	corefonts + "comic32.exe":  "9c6df3feefde26d4e41d4a4fe5db2a89f9123a772594d7f59afd062625cd204e",
	corefonts + "courie32.exe": "bb511d861655dde879ae552eb86b134d6fae67cb58502e6ff73ec5d9151f3384",
	corefonts + "georgi32.exe": "2c2c7dcda6606ea5cf08918fb7cd3f3359e9e84338dc690013f20cd42e930301",
	corefonts + "impact32.exe": "6061ef3b7401d9642f5dfdb5f2b376aa14663f6275e60a51207ad4facf2fccfb",
	corefonts + "times32.exe":  "db56595ec6ef5d3de5c24994f001f03b2a13e37cee27bc25c58f6f43e8f807ab",
	corefonts + "trebuc32.exe": "5a690d9bb8510be1b8b4fe49f1f2319651fe51bbe54775ddddd8ef0bd07fdac9",
	corefonts + "verdan32.exe": "c1cb61255e363166794e47664e2f21af8e3a26cb6346eb8d2ae2fa85dd5aad96",
	corefonts + "webdin32.exe": "64595b5abc1080fba8610c5c34fab5863408e806aafe84653ca8575bed17d75a",
	tahoma:                     "c1be3fb8f0042570be76ec6daa03a99142c88367c1bc810240b85827c715961a",
}

// microsoftCatalog is what DownloadMicrosoftFonts fetches.
var microsoftCatalog = []remoteFile{
	{"arial.ttf", "Arial", corefonts + "arial32.exe", "Arial.TTF", "35c0f3559d8db569e36c31095b8a60d441643d95f59139de40e23fada819b833"},
	{"arialbd.ttf", "Arial", corefonts + "arial32.exe", "Arialbd.TTF", "4044aa6b5bebbc36980206b45b0aaaaa5681552a48bcadb41746d5d1d71fd7b4"},
	{"ariali.ttf", "Arial", corefonts + "arial32.exe", "Ariali.TTF", "70ade233175a6a6675e4501461af9326e6f78b1ffdf787ca0da5ab0fc8c9cfd6"},
	{"arialbi.ttf", "Arial", corefonts + "arial32.exe", "Arialbi.TTF", "2f371cd9d96b3ac544519d85c16dc43ceacdfcea35090ee8ddf3ec5857c50328"},
	{"ariblk.ttf", "Arial Black", corefonts + "arialb32.exe", "AriBlk.TTF", "dad7c04acb26e23dfe4780e79375ca193ddaf68409317e81577a30674668830e"},
	{"times.ttf", "Times New Roman", corefonts + "times32.exe", "Times.TTF", "4e98adeff8ccc8ef4e3ece8d4547e288ff85fdc9c7ca711a4599c234874bbe86"},
	{"timesbd.ttf", "Times New Roman", corefonts + "times32.exe", "Timesbd.TTF", "4357b63cef20c01661a53c5dae70ffd20cb4765503aaed6d38b17a57c5a90bff"},
	{"timesi.ttf", "Times New Roman", corefonts + "times32.exe", "Timesi.TTF", "c25ae529b4cecdbca148b6ccb862ee0abad770af8b1fd29c8dba619d1b8da78a"},
	{"timesbi.ttf", "Times New Roman", corefonts + "times32.exe", "Timesbi.TTF", "192e1b0d18e90334e999a99f8c32808d6a2e74b3698b8cd90c943c2249a46549"},
	{"cour.ttf", "Courier New", corefonts + "courie32.exe", "cour.ttf", "6715838c52f813f3821549d3f645db9a768bd6f3a43d8f85a89cb6875a546c61"},
	{"courbd.ttf", "Courier New", corefonts + "courie32.exe", "courbd.ttf", "edf8a7c5bfcac2e1fe507faab417137cbddc9071637ef4648238d0768c921e02"},
	{"couri.ttf", "Courier New", corefonts + "courie32.exe", "couri.ttf", "f3f6b09855b6700977e214aab5eb9e5be6813976a24f894bd7766e92c732fbe1"},
	{"courbi.ttf", "Courier New", corefonts + "courie32.exe", "courbi.ttf", "66dbfa20b534fba0e203da140fec7276a45a1069e424b1b9c35547538128bbe8"},
	{"verdana.ttf", "Verdana", corefonts + "verdan32.exe", "Verdana.TTF", "96ed14949ca4b7392cff235b9c41d55c125382abbe0c0d3c2b9dd66897cae0cb"},
	{"verdanab.ttf", "Verdana", corefonts + "verdan32.exe", "Verdanab.TTF", "c8f5065ba91680f596af3b0378e2c3e713b95a523be3d56ae185ca2b8f5f0b23"},
	{"verdanai.ttf", "Verdana", corefonts + "verdan32.exe", "Verdanai.TTF", "91b59186656f52972531a11433c866fd56e62ec4e61e2621a2dba70c8f19a828"},
	{"verdanaz.ttf", "Verdana", corefonts + "verdan32.exe", "Verdanaz.TTF", "698e220f48f4a40e77af7eb34958c8fd02f1e18c3ba3f365d93bfa2ed4474c80"},
	{"georgia.ttf", "Georgia", corefonts + "georgi32.exe", "Georgia.TTF", "7d0bb20c632bb59e81a0885f573bd2173f71f73204de9058feb68ce032227072"},
	{"georgiab.ttf", "Georgia", corefonts + "georgi32.exe", "Georgiab.TTF", "82d2fbadb88a8632d7f2e8ad50420c9fd2e7d3cbc0e90b04890213a711b34b93"},
	{"georgiai.ttf", "Georgia", corefonts + "georgi32.exe", "Georgiai.TTF", "1523f19bda6acca42c47c50da719a12dd34f85cc2606e6a5af15a7728b377b60"},
	{"georgiaz.ttf", "Georgia", corefonts + "georgi32.exe", "Georgiaz.TTF", "c983e037d8e4e694dd0fb0ba2e625bca317d67a41da2dc81e46a374e53d0ec8a"},
	{"trebuc.ttf", "Trebuchet MS", corefonts + "trebuc32.exe", "trebuc.ttf", "ec3ffb302488251e1b67fb09dd578b364c5339e27c1cfb26eb627666236453d0"},
	{"trebucbd.ttf", "Trebuchet MS", corefonts + "trebuc32.exe", "Trebucbd.ttf", "f65941f9487c0a0a3b7445996ecbbd24466d7ae76ea2a597ced55f438fa63838"},
	{"trebucit.ttf", "Trebuchet MS", corefonts + "trebuc32.exe", "trebucit.ttf", "db56fdac7d3ba20b7aededcb6ee86c46687489d17b759e1708ea4e2d21e38410"},
	{"trebucbi.ttf", "Trebuchet MS", corefonts + "trebuc32.exe", "trebucbi.ttf", "c0a6bdf31f9f2953b2f08a0c1734c892bc825f0fb17c604d420f7acf203a213b"},
	{"comic.ttf", "Comic Sans MS", corefonts + "comic32.exe", "Comic.TTF", "b82c53776058f291382ff7e008d4675839d2dc21eb295c66391f6fb0655d8fc0"},
	{"comicbd.ttf", "Comic Sans MS", corefonts + "comic32.exe", "Comicbd.TTF", "873361465d994994762d0b9845c99fc7baa2a600442ea8db713a7dd19f8b0172"},
	{"impact.ttf", "Impact", corefonts + "impact32.exe", "Impact.TTF", "00f1fc230ac99f9b97ba1a7c214eb5b909a78660cb3826fca7d64c3af5a14848"},
	{"andalemo.ttf", "Andale Mono", corefonts + "andale32.exe", "AndaleMo.TTF", "48d9bc613917709d3b0e0f4a6d4fe33a5c544c5035dffe9e90bc11e50e822071"},
	{"webdings.ttf", "Webdings", corefonts + "webdin32.exe", "Webdings.TTF", "10d099c88521b1b9e380b7690cbe47b54bb19396ca515358cfdc15ac249e2f5d"},
	{"tahoma.ttf", "Tahoma", tahoma, "tahoma.ttf", "8f8c9a611820737a687ab4624d687abb17b474746cb16f6108ef215bf08eb373"},
	{"tahomabd.ttf", "Tahoma", tahoma, "tahomabd.ttf", "d5eff0e5e6bed9802f4c61bd1bb153bc981eb1e07c8fa2c2f5a7f723fccfd8b7"},
}

// catalog is what Download fetches. Every font is licensed under the SIL OFL
// 1.1, except DejaVu (Bitstream Vera license, also free to use and embed).
var catalog = []remoteFile{
	// Caladea: metric-compatible with Cambria. Latin only.
	{"Caladea-Regular.ttf", "Caladea", googleFonts + "caladea/Caladea-Regular.ttf", "", "f1e899278b7b4491aba5b6a8253c4b04c050cc59b21865be5c37559a775153cd"},
	{"Caladea-Bold.ttf", "Caladea", googleFonts + "caladea/Caladea-Bold.ttf", "", "ae3cb2dcbc925809dd29d2a44e9802211cab66be541bacbfc9c08c74b27c3742"},
	{"Caladea-Italic.ttf", "Caladea", googleFonts + "caladea/Caladea-Italic.ttf", "", "4359a8e24f748b6447b1ff6d7a174febe70961d29f8bb8634b56dacd740a3deb"},
	{"Caladea-BoldItalic.ttf", "Caladea", googleFonts + "caladea/Caladea-BoldItalic.ttf", "", "ccabaa7b7e2fdf253d2b1a5fa699dd8a3df8d835a9eb285ad82631a677eb76c0"},
	// Gelasio: metric-compatible with Georgia. Latin only.
	{"Gelasio-Regular.ttf", "Gelasio", gelasio + "Gelasio-Regular.ttf", "", "48c797fbe0e07c48a18cb962e7bdfa23f19618327dddf54093265328dc9eb39d"},
	{"Gelasio-Bold.ttf", "Gelasio", gelasio + "Gelasio-Bold.ttf", "", "e0ef3addf1acf35f5c6aef2be00d0a2c01363bf70a5950e16650976051a0c462"},
	{"Gelasio-Italic.ttf", "Gelasio", gelasio + "Gelasio-Italic.ttf", "", "1d8bda4ff258bb8251eae4f7d7500c8d97d084136192bb264bc2305168765d1b"},
	{"Gelasio-BoldItalic.ttf", "Gelasio", gelasio + "Gelasio-BoldItalic.ttf", "", "aa87e21b46aef3dcb50a68dc6e1946f0c0a9819d067e4be6a98a6daa8f5b3c16"},
	// Comic Neue: stands in for Comic Sans MS. Latin only.
	{"ComicNeue-Regular.ttf", "Comic Neue", googleFonts + "comicneue/ComicNeue-Regular.ttf", "", "a0ee5a37c8b27c4db0700137d928598b1e23b0089e1546a8961909176b779360"},
	{"ComicNeue-Bold.ttf", "Comic Neue", googleFonts + "comicneue/ComicNeue-Bold.ttf", "", "3e7e5fccfd7e0788f317b43312151c1bd5cf058c9697a8d83eac3939050bd61e"},
	{"ComicNeue-Italic.ttf", "Comic Neue", googleFonts + "comicneue/ComicNeue-Italic.ttf", "", "e06bfd1552f5c9464c5665733ffd69239b0593885dbb9e059688a5900f78cf98"},
	{"ComicNeue-BoldItalic.ttf", "Comic Neue", googleFonts + "comicneue/ComicNeue-BoldItalic.ttf", "", "5c312c2a2fa64eee82f3b87fcfab8f3b12a5e59b043124401d322eb323cfbf16"},
	// DejaVu: Sans stands in for Verdana, Sans Condensed for Tahoma.
	{"DejaVuSans.ttf", "DejaVu Sans", dejaVu, dejaVuDir + "DejaVuSans.ttf", "7da195a74c55bef988d0d48f9508bd5d849425c1770dba5d7bfc6ce9ed848954"},
	{"DejaVuSans-Bold.ttf", "DejaVu Sans", dejaVu, dejaVuDir + "DejaVuSans-Bold.ttf", "e6476c1b80502924294eed40894c5b18e06c181444ca953e5334262df9c27724"},
	{"DejaVuSans-Oblique.ttf", "DejaVu Sans", dejaVu, dejaVuDir + "DejaVuSans-Oblique.ttf", "4af75fa16ee6d3ad43e1ecec41862c24954af26a55c6bb1ebb27bd486a50f5f4"},
	{"DejaVuSans-BoldOblique.ttf", "DejaVu Sans", dejaVu, dejaVuDir + "DejaVuSans-BoldOblique.ttf", "eb436dca0c2594b73d8b603b892e374fdfd8d885d25ffb4f18df4c4c0b49e50f"},
	{"DejaVuSansCondensed.ttf", "DejaVu Sans Condensed", dejaVu, dejaVuDir + "DejaVuSansCondensed.ttf", "8550cd5ca1acb65a8fc7877c46939cd0b4d909f8e7bc1e24716873d918c5e549"},
	{"DejaVuSansCondensed-Bold.ttf", "DejaVu Sans Condensed", dejaVu, dejaVuDir + "DejaVuSansCondensed-Bold.ttf", "38098d0b8edd8430a0f53fb2831b5d36037563a998a620d3c87a4d7591766d20"},
	{"DejaVuSansCondensed-Oblique.ttf", "DejaVu Sans Condensed", dejaVu, dejaVuDir + "DejaVuSansCondensed-Oblique.ttf", "dc3526fa25fae4278c4e34b47d4c049b6923421c70921032a0db11bfe9c1b0cf"},
	{"DejaVuSansCondensed-BoldOblique.ttf", "DejaVu Sans Condensed", dejaVu, dejaVuDir + "DejaVuSansCondensed-BoldOblique.ttf", "43f9fd6b77edf174553cf94857c77c63de7ddb0c6751fd1390e73223518e128f"},
	{"DejaVuSerif.ttf", "DejaVu Serif", dejaVu, dejaVuDir + "DejaVuSerif.ttf", "42d1edeb7952f31b1f96d767ed7030b08a39e0c372b0071641518864e2bffb51"},
	{"DejaVuSerif-Bold.ttf", "DejaVu Serif", dejaVu, dejaVuDir + "DejaVuSerif-Bold.ttf", "c47b5527bcdc8dcf9ea8c77054454c5a884beaca2f44851a2a823ee639cbf07f"},
	{"DejaVuSerif-Italic.ttf", "DejaVu Serif", dejaVu, dejaVuDir + "DejaVuSerif-Italic.ttf", "2e39b1d50f90b933b00c7bb54a96afd3f86419b3d717c7cf202e36f2d4973e47"},
	{"DejaVuSerif-BoldItalic.ttf", "DejaVu Serif", dejaVu, dejaVuDir + "DejaVuSerif-BoldItalic.ttf", "8d3dd3d31350309042ed226af82b34539bd773518e6107cb352712853ba80308"},
	{"DejaVuSansMono.ttf", "DejaVu Sans Mono", dejaVu, dejaVuDir + "DejaVuSansMono.ttf", "b4a6c3e4faab8773f4ff761d56451646409f29abedd68f05d38c2df667d3c582"},
	{"DejaVuSansMono-Bold.ttf", "DejaVu Sans Mono", dejaVu, dejaVuDir + "DejaVuSansMono-Bold.ttf", "bce60f1b4421acd9ea51ba6623d7024ecbe6817a953e3654df62a5e6bdf8f769"},
	{"DejaVuSansMono-Oblique.ttf", "DejaVu Sans Mono", dejaVu, dejaVuDir + "DejaVuSansMono-Oblique.ttf", "742097840c541870e8d6dc5c9b37bb1ceeea6c0dedd1d475faf903ef9df734b0"},
	{"DejaVuSansMono-BoldOblique.ttf", "DejaVu Sans Mono", dejaVu, dejaVuDir + "DejaVuSansMono-BoldOblique.ttf", "91713a71d550bba22c2a6b2bb2a9ad8f9a159e12e4e9f0a5b2677998ba21213e"},
	// Families documents name directly; all cover Cyrillic.
	{"PT_Sans-Web-Regular.ttf", "PT Sans", googleFonts + "ptsans/PT_Sans-Web-Regular.ttf", "", "9cc831490532009bae2b3ce0d39c62adfc889060beb421593bfd9d2396d0f10a"},
	{"PT_Sans-Web-Bold.ttf", "PT Sans", googleFonts + "ptsans/PT_Sans-Web-Bold.ttf", "", "3128bd5ecf01816e59a23d54c57a7a6b14615b07db53ff277c77376010265b05"},
	{"PT_Sans-Web-Italic.ttf", "PT Sans", googleFonts + "ptsans/PT_Sans-Web-Italic.ttf", "", "5a90fe2d0cd798700935240580bdcc12c0ffc9102c0c7163b3418e13bc21debd"},
	{"PT_Sans-Web-BoldItalic.ttf", "PT Sans", googleFonts + "ptsans/PT_Sans-Web-BoldItalic.ttf", "", "81ac221cdd02bccfa679c74adb122478e9d092e65a722e31ca11469961483785"},
	{"PT_Serif-Web-Regular.ttf", "PT Serif", googleFonts + "ptserif/PT_Serif-Web-Regular.ttf", "", "a4951fade06ff8f09b7673aa81ffb65a8cd409e24d3289a6dc670bc4dda2557a"},
	{"PT_Serif-Web-Bold.ttf", "PT Serif", googleFonts + "ptserif/PT_Serif-Web-Bold.ttf", "", "038ba7336bd7ea14f12ad155bed51a4345cac5153275d521dec3ba04021c526e"},
	{"PT_Serif-Web-Italic.ttf", "PT Serif", googleFonts + "ptserif/PT_Serif-Web-Italic.ttf", "", "f57e95ff9dc85691a3b2e193f2028db36f6663939a46c0fc4f286d618b80b7ce"},
	{"PT_Serif-Web-BoldItalic.ttf", "PT Serif", googleFonts + "ptserif/PT_Serif-Web-BoldItalic.ttf", "", "f003788ba08981eb0988b3557a6f224a53dab49c20e283e8b74d5af3c466f8be"},
	{"PTM55FT.ttf", "PT Mono", googleFonts + "ptmono/PTM55FT.ttf", "", "cbe732b3b8fd211fd986ebdfc9b870ddeca4faab0bb5425fc509b37f9b4ac804"},
	{"FiraSans-Regular.ttf", "Fira Sans", googleFonts + "firasans/FiraSans-Regular.ttf", "", "c29556a2719bf613ef3d5e070e40d903a8965d9c081beca1375dc1e6e0f93c23"},
	{"FiraSans-Bold.ttf", "Fira Sans", googleFonts + "firasans/FiraSans-Bold.ttf", "", "a4d8e149ecdd4874a0726eb0af894488b3b31c423d6b0017c8f415ed1b795b45"},
	{"FiraSans-Italic.ttf", "Fira Sans", googleFonts + "firasans/FiraSans-Italic.ttf", "", "a4204f9a8029c1c55daa5385ac95936f74abea3e232770f1fb2862a19d1dfa13"},
	{"FiraSans-BoldItalic.ttf", "Fira Sans", googleFonts + "firasans/FiraSans-BoldItalic.ttf", "", "1b96c16db9819bcaa9d588068bb397bb6e977c4874965d3d50d789a1943757eb"},
	{"IBMPlexSerif-Regular.ttf", "IBM Plex Serif", googleFonts + "ibmplexserif/IBMPlexSerif-Regular.ttf", "", "e882efa9c41949a528ac2369079ec5ef050c1c996bbd0bacce3c3326d44cf80d"},
	{"IBMPlexSerif-Bold.ttf", "IBM Plex Serif", googleFonts + "ibmplexserif/IBMPlexSerif-Bold.ttf", "", "534c02c295999dd86e770457ece1d43db0de9256dd98bf741426f63ae904209e"},
	{"IBMPlexSerif-Italic.ttf", "IBM Plex Serif", googleFonts + "ibmplexserif/IBMPlexSerif-Italic.ttf", "", "4b75b38be4673527231f49c48818d090c913d5042dd5c747b525bf6185d29ecb"},
	{"IBMPlexSerif-BoldItalic.ttf", "IBM Plex Serif", googleFonts + "ibmplexserif/IBMPlexSerif-BoldItalic.ttf", "", "2a32b76ac19c1942bf5942dbbd2a1566e5f1ae9833e421ebcf36d3522715e153"},
	{"Lato-Regular.ttf", "Lato", googleFonts + "lato/Lato-Regular.ttf", "", "d636e4683231f931eda222d588e944d082bfd3bdba02f928bee461c0f185b251"},
	{"Lato-Bold.ttf", "Lato", googleFonts + "lato/Lato-Bold.ttf", "", "8a0aace75d33794eece4b28187bfc1df0bbd2888b5d8a56e01788c8d65d16be1"},
	{"Lato-Italic.ttf", "Lato", googleFonts + "lato/Lato-Italic.ttf", "", "e399c44efe1387100531d26c7e4800c5d12251b890d6654a3098c7c679cb1786"},
	{"Lato-BoldItalic.ttf", "Lato", googleFonts + "lato/Lato-BoldItalic.ttf", "", "62c1b7f0d2e74b45960154c3520efc337b553db0961bfdc950d5618334596cc8"},
}

// substitutes maps a Word font name, lower-cased, to the catalog family that
// stands in for it when the font itself is not installed.
var substitutes = map[string]string{
	"cambria":       "Caladea",
	"georgia":       "Gelasio",
	"comic sans ms": "Comic Neue",
	"verdana":       "DejaVu Sans",
	"tahoma":        "DejaVu Sans Condensed",
}

// Substitute returns the downloadable family that stands in for the Word font
// name (Caladea for Cambria, DejaVu Sans for Verdana, ...), or "" when there
// is none. Case and surrounding spaces in name are ignored.
func Substitute(name string) string {
	return substitutes[strings.ToLower(strings.TrimSpace(name))]
}

// DefaultDir is the directory Download is usually given: docx2pdf/fonts in
// the user's cache directory (os.UserCacheDir).
func DefaultDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "docx2pdf", "fonts"), nil
}

// Download fetches the open-source fonts that stand in for common Word fonts
// with no embedded replacement (see Substitute), and a few families documents
// name directly (PT Sans, PT Serif, PT Mono, Fira Sans, IBM Plex Serif,
// Lato, DejaVu), into dir, creating it if needed. Pass dir to
// document.Converter.WithFontDir to use them.
//
// Each file is checked against a pinned SHA-256 checksum before it is
// written. A file already in dir with the right checksum is not fetched
// again, so calling Download at every start costs only reading about 17 MB
// of fonts once they are there. A font that fails does not stop the others:
// the returned error joins every failure, and dir keeps every font that
// succeeded. Download is safe to call concurrently with conversions that
// read dir: each file appears complete or not at all.
func Download(ctx context.Context, dir string) error {
	return download(ctx, &http.Client{Timeout: 2 * time.Minute}, dir, catalog, archiveSums)
}

// DownloadMicrosoftFonts fetches Microsoft's TrueType core fonts for the Web -
// Arial, Arial Black, Times New Roman, Courier New, Verdana, Georgia, Trebuchet
// MS, Comic Sans MS, Impact, Andale Mono and Webdings - and Tahoma into dir,
// the way winetricks' corefonts and Debian's ttf-mscorefonts-installer do:
// the installers Microsoft published, from the mirrors winetricks uses, with
// the fonts extracted from them here. With these, text in those fonts is drawn
// in the fonts themselves rather than in metric-compatible replacements, and
// Verdana and Tahoma no longer fall back to DejaVu. All but Webdings cover
// Cyrillic.
//
// The fonts are Microsoft's, under its End-User License Agreement (shown by
// each installer, and reproduced in the corefonts project); calling this
// function means accepting it. They are downloaded to the user's machine,
// never redistributed by this package. Each installer and each font is
// checked against a pinned SHA-256 checksum; otherwise it behaves as Download.
func DownloadMicrosoftFonts(ctx context.Context, dir string) error {
	return download(ctx, &http.Client{Timeout: 2 * time.Minute}, dir, microsoftCatalog, archiveSums)
}

// download fetches files into dir. archiveSums holds the checksum of every
// archive a file is extracted from, verified before the archive is opened.
func download(ctx context.Context, client *http.Client, dir string, files []remoteFile, archiveSums map[string]string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("docx2pdf: font directory: %w", err)
	}
	archives := make(map[string][]byte) // by url: an archive is fetched once
	var errs []error
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		path := filepath.Join(dir, f.name)
		if data, err := os.ReadFile(path); err == nil && checksum(data) == f.sha256 {
			continue
		}
		data, err := fetchFont(ctx, client, f, archives, archiveSums)
		if err == nil && checksum(data) != f.sha256 {
			err = fmt.Errorf("checksum mismatch: got %s, want %s", checksum(data), f.sha256)
		}
		if err == nil {
			err = writeAtomic(path, data)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("docx2pdf: download font %s: %w", f.name, err))
		}
	}
	return errors.Join(errs...)
}

func checksum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// fetchFont returns f's bytes: the body at f.url, or f.member of the archive
// there - a zip file, or a Microsoft cabinet (a .cab, or a self-extracting
// .exe holding one).
func fetchFont(ctx context.Context, client *http.Client, f remoteFile, archives map[string][]byte, archiveSums map[string]string) ([]byte, error) {
	if f.member == "" {
		return get(ctx, client, f.url)
	}
	archive := archives[f.url]
	if archive == nil {
		body, err := get(ctx, client, f.url)
		if err != nil {
			return nil, err
		}
		if want := archiveSums[f.url]; checksum(body) != want {
			return nil, fmt.Errorf("archive %s: checksum mismatch: got %s, want %q", f.url, checksum(body), want)
		}
		archive = body
		archives[f.url] = archive
	}
	if !strings.HasSuffix(f.url, ".zip") {
		return cab.Extract(archive, f.member)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}
	rc, err := zr.Open(f.member)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }() // read-only: the checksum catches a bad read
	return readCapped(rc)
}

func get(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() // read-only: a close error loses nothing
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return readCapped(resp.Body)
}

func readCapped(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownload {
		return nil, fmt.Errorf("larger than %d bytes", maxDownload)
	}
	return data, nil
}

// writeAtomic writes data to path through a temporary file in the same
// directory, so a reader never sees a partly written font.
func writeAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".download-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name()) // best effort: the error that matters is already returned
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() // the write error is the one to report
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
