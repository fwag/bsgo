#
# spec file for package ImageMagick
#
# Copyright (c) 2025 SUSE LLC
#
# All modifications and additions to the file contributed by third parties
# remain the property of their copyright owners, unless otherwise agreed
# upon. The license for this file, and modifications and additions to the
# file, is the same license as for the pristine package itself (unless the
# license for the pristine package is not an Open Source License, in which
# case the license is the MIT License). An "Open Source License" is a
# license that conforms to the Open Source Definition (Version 1.9)
# published by the Open Source Initiative.

# Please submit bugfixes or comments via https://bugs.opensuse.org/
#


%global flavor          @BUILD_FLAVOR@%{nil}

%define debug_build    0
%define asan_build     0
%define mfr_version    7.1.2
%define mfr_revision   0
%define quantum_depth  16
%define source_version %{mfr_version}-%{mfr_revision}
%define clibver        10
%define cwandver       10
%define cxxlibver      5
%define libspec        -7_Q%{quantum_depth}HDRI
%define config_dir     IM-7
%define test_verbose   1
# bsc#1088463
%define urw_base35_fonts 0
# do/don't pull djvulibre dependency
%bcond_without djvu

Name:           ImageMagick
Version:        %{mfr_version}.%{mfr_revision}
Release:        0
Summary:        Viewer and Converter for Images
License:        ImageMagick
Group:          Productivity/Graphics/Other
URL:            https://imagemagick.org/
Source0:        https://imagemagick.org/archive/releases/ImageMagick-%{source_version}.tar.xz
Source1:        baselibs.conf
Source2:        https://imagemagick.org/archive/releases/ImageMagick-%{source_version}.tar.xz.asc
Source3:        ImageMagick.keyring
Source4:        ImageMagick-SUSE-security-policy.xml
# do not block read access to own config files
Patch0:         ImageMagick_policy_etc.patch
# library installation
Patch2:         ImageMagick-library-installable-in-parallel.patch
# disable failing tests
Patch5:         ImageMagick-s390x-disable-tests.patch
# bsc#1247475 (https://github.com/ImageMagick/ImageMagick/issues/8261)
Patch6:         ImageMagick-filename-placeholder-regression-1.patch
Patch7:         ImageMagick-filename-placeholder-regression-2.patch
Patch8:         ImageMagick-filename-placeholder-regression-3.patch
# CVE-2025-55005 [bsc#1248077], heap buffer overflow when transforming from Log to sRGB colorspaces
Patch9:         ImageMagick-CVE-2025-55005.patch
# CVE-2025-55154 [bsc#1248078], integer overflow when performing magnified size calculations in ReadOneMNGIMage can lead to out-of-bounds write
Patch10:         ImageMagick-CVE-2025-55154.patch
# CVE-2025-55004 [bsc#1248076], heap buffer over-read in in ReadOneMNGIMage when processing images with separate alpha channels
Patch11:         ImageMagick-CVE-2025-55004.patch
# CVE-2025-55160 [bsc#1248079], undefined behavior due to function-type-mismatch in CloneSplayTree
Patch12:         ImageMagick-CVE-2025-55160.patch
# CVE-2025-55212 [bsc#1248767], division-by-zero in ThumbnailImage() when passing a geometry string containing only a colon to `montage -geometry`
Patch13:         ImageMagick-CVE-2025-55212.patch
# CVE-2025-57803 [bsc#1248784], 32-bit integer overflow can lead to heap out-of-bounds (OOB) write
Patch14:         ImageMagick-CVE-2025-57803.patch
# CVE-2025-55298 [bsc#1248780], format string bug vulnerability can lead to heap overflow
Patch15:         ImageMagick-CVE-2025-55298.patch
# CVE-2025-57807 [bsc#1249362], BlobStream Forward-Seek Under-Allocation
Patch16:         ImageMagick-CVE-2025-57807.patch
# CVE-2025-62171 [bsc#1252282], incomplete fix for integer overflow in BMP Decoder
Patch17:         ImageMagick-CVE-2025-62171.patch
# CVE-2025-62594 [bsc#1252749], unsigned underflow and division-by-zero can lead to OOB pointer arithmetic and process crash
Patch18:         ImageMagick-CVE-2025-62594.patch
# CVE-2025-65955 [bsc#1254435], ImageMagick use-after-free/double-free
Patch19:         ImageMagick-CVE-2025-65955.patch
# CVE-2025-66628 [bsc#1254820], ImageMagick Integer Overflow leading to out of bounds read (32-bit only)
Patch20:         ImageMagick-CVE-2025-66628.patch
# CVE-2025-68618 [bsc#1255821], read a malicious SVG file may result in a DoS attack
Patch21:         ImageMagick-CVE-2025-68618.patch
# CVE-2025-68950 [bsc#1255822], check for circular references in mvg files may lead to stack overflow
Patch22:         ImageMagick-CVE-2025-68950.patch
# CVE-2025-69204 [bsc#1255823], an integer overflow can lead to a DoS attack
Patch23:         ImageMagick-CVE-2025-69204.patch
# CVE-2026-23876 [bsc#1256962], maliciously crafted image can lead to heap buffer overflow
Patch24:         ImageMagick-CVE-2026-23876.patch
# CVE-2026-22770 [bsc#1256969], improper pointer initialization can cause denial of service
Patch25:         ImageMagick-CVE-2026-22770.patch
# CVE-2026-23952 [bsc#1257076], processing comment tag can cause null pointer dereference
Patch26:         ImageMagick-CVE-2026-23952.patch
# CVE-2026-23874 [bsc#1256976], manipulation of digital images can lead to stack overflow
Patch27:         ImageMagick-CVE-2026-23874.patch
# CVE-2026-24481: Possible Heap Information Disclosure in PSD ZIP Decompression (bsc#1258743)
Patch28:         ImageMagick-CVE-2026-24481.patch
# CVE-2026-25794: Heap-buffer-overflow via signed integer overflow in `WriteUHDRImage` when writing UHDR images with large dimensions (bsc#1258749)
Patch29:         ImageMagick-CVE-2026-25794.patch
# CVE-2026-25796: Memory leak of watermark Image object in ReadSTEGANOImage on multiple error/early-return paths (bsc#1258757)
Patch30:         ImageMagick-CVE-2026-25796.patch
# CVE-2026-25637: Denial of Service via crafted image due to memory leak (bsc#1258759)
Patch31:         ImageMagick-CVE-2026-25637.patch
# CVE-2026-25576: Out of bounds read in multiple coders that read raw pixel data (bsc#1258748)
Patch32:         ImageMagick-CVE-2026-25576.patch
# CVE-2026-26983: Invalid MSL map can result in a use after free (bsc#1258763)
Patch33:         ImageMagick-CVE-2026-26983.patch
# CVE-2026-26284: Heap overflow in pcd decoder leads to out of bounds read. (bsc#1258765)
Patch34:         ImageMagick-CVE-2026-26284.patch
# CVE-2026-26283: Possible infinite loop in JPEG encoder when using `jpeg:extent` (bsc#1258767)
Patch35:         ImageMagick-CVE-2026-26283.patch
# CVE-2026-25967: Stack buffer overflow in FTXT reader via oversized integer field (bsc#1258779)
Patch36:         ImageMagick-CVE-2026-25967.patch
# CVE-2026-25989: Integer overflow or wraparound and incorrect conversion between numeric types in the internal SVG decoder (bsc#1258771)
Patch37:         ImageMagick-CVE-2026-25989.patch
# CVE-2026-25968: MSL attribute stack buffer overflow leads to out of bounds write (bsc#1258776)
Patch38:         ImageMagick-CVE-2026-25968.patch
# CVE-2026-25799: Division-by-Zero in YUV sampling factor validation leads to crash (bsc#1258786)
Patch39:         ImageMagick-CVE-2026-25799.patch
# CVE-2026-25798: NULL Pointer Dereference in ClonePixelCacheRepository via crafted image (bsc#1258787)
Patch40:         ImageMagick-CVE-2026-25798.patch
# CVE-2026-25795: Denial of Service due to NULL pointer dereference during temporary file creation failure (bsc#1258792)
Patch41:         ImageMagick-CVE-2026-25795.patch
# CVE-2026-26066: Infinite loop when writing IPTCTEXT leads to denial of service via crafted profile (bsc#1258769)
Patch42:         ImageMagick-CVE-2026-26066.patch
# CVE-2026-25638: Denial of Service due to memory leak in image processing (bsc#1258793)
Patch43:         ImageMagick-CVE-2026-25638.patch
# CVE-2026-25982: Heap Out-of-Bounds Read in DCM Decoder (bsc#1258772)
Patch44:         ImageMagick-CVE-2026-25982.patch
# CVE-2026-25898: Information disclosure or denial of service via crafted image with invalid pixel index (bsc#1258807)
Patch45:         ImageMagick-CVE-2026-25898.patch
# CVE-2026-24485: denial of service via malformed PCD file processing (bsc#1258791)
Patch46:         ImageMagick-CVE-2026-24485.patch
# CVE-2026-25965: Policy bypass through path traversal allows reading restricted content despite secured policy (bsc#1258785)
Patch47:         ImageMagick-CVE-2026-25965.patch
# CVE-2026-25985: Memory allocation with excessive without limits in the internal SVG decoder (bsc#1258812)
Patch48:         ImageMagick-CVE-2026-25985.patch
# CVE-2026-25987: Memory disclosure and denial of service via crafted MAP files (bsc#1258821)
Patch49:         ImageMagick-CVE-2026-25987.patch
# CVE-2026-25986: Denial of Service via malicious YUV image processing (bsc#1258818)
Patch50:         ImageMagick-CVE-2026-25986.patch
# CVE-2026-25988: Denial of Service due to memory leak in image processing (bsc#1258810)
Patch51:         ImageMagick-CVE-2026-25988.patch
# CVE-2026-25983: Denial of service via crafted MSL script (bsc#1258805)
Patch52:         ImageMagick-CVE-2026-25983.patch
# CVE-2026-25897: Out-of-bounds heap write via integer overflow in sun decoder (bsc#1258799)
Patch53:         ImageMagick-CVE-2026-25897.patch
# CVE-2026-24484: denial of service vulnerability via multi-layer nested MVG to SVG conversion (bsc#1258790)
Patch54:         ImageMagick-CVE-2026-24484.patch
# CVE-2026-25969: Memory Leak in coders/ashlar.c (bsc#1258775)
Patch55:         ImageMagick-CVE-2026-25969.patch
# CVE-2026-25970: Memory corruption and denial of service via signed integer overflow in SIXEL decoder. (bsc#1258802)
Patch56:         ImageMagick-CVE-2026-25970.patch
# CVE-2026-25797: Code injection in various encoders (bsc#1258770)
Patch57:         ImageMagick-CVE-2026-25797.patch
# CVE-2026-25971: Stack overflow in ProcessMSLScript (bsc#1258774)
Patch58:         ImageMagick-CVE-2026-25971.patch
# CVE-2026-27799: ImageMagick has a heap Buffer Over-read in its DJVU image format handler (bsc#1259017)
Patch59:         ImageMagick-CVE-2026-27799.patch
# CVE-2026-27798: Heap Buffer Over-read in WaveletDenoise when processing small images (bsc#1259018)
Patch60:         ImageMagick-CVE-2026-27798.patch
# CVE-2026-28693: integer overflow in the DIB coder can lead to an out-of-bounds read or write (bsc#1259466)
Patch61:         ImageMagick-CVE-2026-28693.patch
# CVE-2026-30883: missing bounds check when encoding a PNG image can lead to a heap buffer over-write (bsc#1259467)
Patch62:         ImageMagick-CVE-2026-30883.patch
# CVE-2026-30929: improper use of fixed-size stack buffer in `MagnifyImage`can lead to a stack buffer overflow (bsc#1259468)
Patch63:         ImageMagick-CVE-2026-30929.patch
# CVE-2026-30931: value truncation in the UHDR encoder can lead to a heap buffer overflow (bsc#1259469)
Patch64:         ImageMagick-CVE-2026-30931.patch
# CVE-2026-28493: integer overflow in the SIXEL decoder leads to out-of-bounds write (bsc#1259446)
Patch65:         ImageMagick-CVE-2026-28493.patch
# CVE-2026-31853: heap buffer overflow leads to crash in the SFW decoder of 32-bit systems when processing extremely large images (bsc#1259528)
Patch66:         ImageMagick-CVE-2026-31853.patch
# CVE-2026-28494: missing bounds checks in the morphology kernel parsing functions can lead to a stack buffer overflow (bsc#1259447)
Patch67:         ImageMagick-CVE-2026-28494.patch
# CVE-2026-28687: heap use-after-free vulnerability in the MSL decoder via a crafted MSL file (bsc#1259450)
Patch68:         ImageMagick-CVE-2026-28687.patch
# CVE-2026-28686: undersized output buffer allocation in the PCL encoder can lead to a heap buffer overflow (bsc#1259448)
Patch69:         ImageMagick-CVE-2026-28686.patch
# CVE-2026-28691: missing check in the JBIG decoder can lead to an uninitialized pointer dereference (bsc#1259455)
Patch70:         ImageMagick-CVE-2026-28691.patch
# CVE-2026-28690: missing bounds check in the MNG encoder can lead to a stack buffer overflow (bsc#1259456)
Patch71:         ImageMagick-CVE-2026-28690.patch
# CVE-2026-28692: 32-bit integer overflow in MAT decoder can lead to a heap buffer over-read (bsc#1259457)
Patch72:         ImageMagick-CVE-2026-28692.patch
# CVE-2026-28688: heap use-after-free in the MSL encoder when a cloned image is destroyed twice (bsc#1259451)
Patch73:         ImageMagick-CVE-2026-28688.patch
# CVE-2026-30935: heap-based buffer over-read in BilateralBlurImage (bsc#1259497)
Patch74:         ImageMagick-CVE-2026-30935.patch
# CVE-2026-30936: Heap Buffer Overflow in WaveletDenoiseImage (bsc#1259464)
Patch75:         ImageMagick-CVE-2026-30936.patch
# CVE-2026-30937: Heap buffer overflow in XWD encoder due to CARD32 arithmetic overflow (bsc#1259463)
Patch76:         ImageMagick-CVE-2026-30937.patch
# CVE-2026-28689: `domain=path` authorization is checked before final file open/use and allows for read/write bypass via symlink swaps (bsc#1259452)
Patch77:         ImageMagick-CVE-2026-28689.patch
# CVE-2026-32259: memory allocation fails can lead to out of bound write (bsc#1259612)
Patch78:         ImageMagick-CVE-2026-32259.patch
# CVE-2026-32636: Denial of Service via out-of-bounds write in NewXMLTree method (bsc#1259872)
Patch79:         ImageMagick-CVE-2026-32636.patch
# CVE-2026-33535: Out-of-Bounds write of a zero byte in X11 display interaction (bsc#1260874)
Patch80:         ImageMagick-CVE-2026-33535.patch
# CVE-2026-33536: Denial of Service via out-of-bounds write (bsc#1260879)
Patch81:         ImageMagick-CVE-2026-33536.patch
# CVE-2026-33905: Denial of service via out-of-bounds read in -sample operation [bsc#1262097]
Patch82:         ImageMagick-CVE-2026-33905.patch
# CVE-2026-33901: Denial of Service due to heap buffer overflow in MVG decoder [bsc#1262155]
Patch83:         ImageMagick-CVE-2026-33901.patch
# CVE-2026-33900: Denial of Service via integer truncation in viff encoder [bsc#1262156]
Patch84:         ImageMagick-CVE-2026-33900.patch
# CVE-2026-33902: Denial of Service via deeply nested expression in FX parser [bsc#1262153]
Patch85:         ImageMagick-CVE-2026-33902.patch
# CVE-2026-40312: Denial of Service via malicious MSL file processing [bsc#1262149]
Patch86:         ImageMagick-CVE-2026-40312.patch
# CVE-2026-34238: Denial of Service via integer overflow in despeckle operation [bsc#1262147]
Patch87:         ImageMagick-CVE-2026-34238.patch
# CVE-2026-40169: Denial of Service via crafted image leading to out-of-bounds write [bsc#1262150]
Patch88:         ImageMagick-CVE-2026-40169.patch
# CVE-2026-40310: Denial of service via heap out-of-bounds write in JP2 encoder [bsc#1262148]
Patch89:         ImageMagick-CVE-2026-40310.patch
# CVE-2026-40183: Denial of Service via heap write overflow in JXL encoder [bsc#1262145]
Patch90:         ImageMagick-CVE-2026-40183.patch
# CVE-2026-40311: Denial of Service via heap use-after-free in XMP profile processing [bsc#1262146]
Patch91:         ImageMagick-CVE-2026-40311.patch
# CVE-2026-33899: Denial of Service via out-of-bounds write in XML parsing [bsc#1262154]
Patch92:         ImageMagick-CVE-2026-33899.patch
# CVE-2026-33908: Denial of Service via deeply nested XML file processing [bsc#1262152]
Patch93:         ImageMagick-CVE-2026-33908.patch
# CVE-2026-42050: Stack buffer overflow in XTileImage [bsc#1265048]
Patch94:         ImageMagick-CVE-2026-42050.patch
# CVE-2026-49218: denial of service due to a missing check in the DCM decoder [bsc#1268110]
Patch95:         ImageMagick-CVE-2026-49218.patch
# CVE-2026-53461: out of bounds heap write due to an incorrect loop in the ICON decoder [bsc#1268107]
Patch96:         ImageMagick-CVE-2026-53461.patch
# CVE-2026-53460: out-of-Memory condition due to a missing check for maximum memory request in AcquireAlignedMemory [bsc#1268108]
Patch97:         ImageMagick-CVE-2026-53460.patch
# CVE-2026-46522: denial of service via crafted MIFF file due to a missing check in the MIFF decoder [bsc#1268126]
Patch98:         ImageMagick-CVE-2026-46522.patch
# CVE-2026-48994: heap buffer over-write due to a missing check of a return value in the MAT decoder on 32-bit systems [bsc#1268111]
Patch99:         ImageMagick-CVE-2026-48994.patch
# CVE-2026-46520: Denial of Service via out-of-bounds write when processing multiple images [bsc#1268112]
Patch100:         ImageMagick-CVE-2026-46520.patch
# CVE-2026-42326: Information disclosure via malicious IPTC input file [bsc#1268092]
Patch101:         ImageMagick-CVE-2026-42326.patch
# CVE-2026-45031: Denial of Service due to resource policy bypass in PSD decoder [bsc#1268094]
Patch102:         ImageMagick-CVE-2026-45031.patch
# CVE-2026-45359: Information Disclosure via Invalid Connected-Components Value [bsc#1268095]
Patch103:         ImageMagick-CVE-2026-45359.patch
# CVE-2026-48724: Heap Buffer Underwrite in Floyd-Steinberg depth dithering [bsc#1268116]
Patch104:         ImageMagick-CVE-2026-48724.patch
# CVE-2026-45624: Data exposure due to image processing vulnerability [bsc#1268096]
Patch105:         ImageMagick-CVE-2026-45624.patch
# CVE-2026-46692: heap buffer over-write in the server process via an attacker who can connect to a magick -distribute-cache service [bsc#1268120]
Patch106:         ImageMagick-CVE-2026-46692.patch
# CVE-2026-53464: small memory leak due to providing invalid options to the wand option parser [bsc#1268103]
Patch107:         ImageMagick-CVE-2026-53464.patch
# CVE-2026-48734: Stack Overflow in MVG decoder [bsc#1268122]
Patch108:         ImageMagick-CVE-2026-48734.patch
# CVE-2026-53463: null pointer deference due to passing incorrect arguments in the distort operation [bsc#1268105]
Patch109:         ImageMagick-CVE-2026-53463.patch
# CVE-2026-48733: Infinite Loop in subimage-search with crafted image [bsc#1268119]
Patch110:         ImageMagick-CVE-2026-48733.patch
# CVE-2026-46557: stack overflow can occur in the fx operation by passing a crafted argument due to a missing depth check [bsc#1268123]
Patch111:         ImageMagick-CVE-2026-46557.patch
# CVE-2026-46521: out of bounds write can occur due to a missing check when using LZMA compression in the MIFF encoder [bsc#1268124]
Patch112:         ImageMagick-CVE-2026-46521.patch
# CVE-2026-46523: heap-use-after-free via a crafted MSL image [bsc#1268125]
Patch113:         ImageMagick-CVE-2026-46523.patch
# CVE-2026-45358: off by one in the meta encoder could result in an out of bounds read of a single byte in the meta encoder [bsc#1268102]
Patch114:         ImageMagick-CVE-2026-45358.patch
# CVE-2026-46559: heap buffer over-write of a single byte when specifying certain options due to n incorrect check in the JP2 [bsc#1268121]
Patch115:         ImageMagick-CVE-2026-46559.patch
# CVE-2026-47165: distributed pixel cache was originally designed to operate without a challenge–response authentication model [bsc#1268114]
Patch116:         ImageMagick-CVE-2026-47165.patch
# CVE-2026-47166 [bsc#1268113], CVE-2026-46693 [bsc#1268117]
Patch117:         ImageMagick-distribute-cache-update.patch
# CVE-2026-45664: Denial of Service due to excessive resource use in MNG coder [bsc#1268101]
Patch118:         ImageMagick-CVE-2026-45664.patch
# CVE-2026-56368: memory leak in multiple coders that write raw pixel data [bsc#1269064]
Patch119:         ImageMagick-CVE-2026-56368.patch
# CVE-2026-56376: heap use-after-free in the meta coder can lead to denial of service via specially crafted image files [bsc#1268880]
Patch120:         ImageMagick-CVE-2026-56376.patch
# CVE-2026-56371: memory leak in coders/txt.c when processing TXT files with texture attributes [bsc#1268879]
Patch121:         ImageMagick-CVE-2026-56371.patch
# CVE-2026-56367: ImageMagick contains an integer overflow in the PSB (PSD v2) RLE decoding path that causes a heap out-of-bounds read. [bsc#1268645]
Patch122:         ImageMagick-CVE-2026-56367.patch
# GHSA-3j4x-rwrx-xxj9: ImageMagick has a possible use-after-free write in its PDB decoder
Patch123:         ImageMagick-GHSA-3j4x-rwrx-xxj9.patch
# CVE-2026-56370: out-of-bounds access in `ConnectedComponentsImage()` when processing connected-components:* artifacts with invalid indices [bsc#1269063]
Patch124:         ImageMagick-CVE-2026-56370.patch
# CVE-2026-56379: arbitrary MVG drawing command injection via the SVG decoder when processing specially crafted SVG files [bsc#1268878]
Patch125:         ImageMagick-CVE-2026-56379.patch
# CVE-2026-56364: Memory Leak in LoadOpenCLDeviceBenchmark() when parsing malformed XML [bsc#1270003]
Patch126:         ImageMagick-CVE-2026-56364.patch
# CVE-2026-55597: Heap Buffer Over-Write in JP2 encoder when due to incorrect handling of arguments [bsc#1270080]
Patch127:         ImageMagick-CVE-2026-55597.patch
# CVE-2026-55594: Stack Overflow in MVG decoder due to missing depth check [bsc#1270077]
Patch128:         ImageMagick-CVE-2026-55594.patch
# CVE-2026-56361: Off-by-one origin validation in allows out-of-bounds read in morphology processing [bsc#1270001]
Patch129:         ImageMagick-CVE-2026-56361.patch
# CVE-2026-53466: Heap Buffer Over-Read in XCF decoder due to integer conversion overflow [bsc#1270073]
Patch130:         ImageMagick-CVE-2026-53466.patch
# CVE-2026-56363: Division by Zero in binomial kernel [bsc#1270002]
Patch131:         ImageMagick-CVE-2026-56363.patch
# CVE-2026-55595: Infinite Loop in connected-components when providing invalid arguments [bsc#1270079]
Patch132:         ImageMagick-CVE-2026-55595.patch
# CVE-2026-53467: Information Disclosure in MNG decoder because allocated memory is left unchanged [bsc#1270074]
Patch133:         ImageMagick-CVE-2026-53467.patch
# CVE-2026-56374: Heap-buffer-overflow in FTXT encoder [bsc#1271099]
Patch134:         ImageMagick-CVE-2026-56374.patch
# CVE-2026-56362: Heap-buffer-overflow read in GetPixelIndex due to metadata-cache desynchronization in OpenPixelCache [bsc#1271100]
Patch135:         ImageMagick-CVE-2026-56362.patch
# CVE-2026-55628: Policy Bypass in concatenate operation due to missing checks [bsc#1270081]
Patch136:         ImageMagick-CVE-2026-55628.patch
# CVE-2026-56377: Policy Bypass can create or truncate files [bsc#1270006]
Patch137:         ImageMagick-CVE-2026-56377.patch
# CVE-2026-61857: Heap-use-after-free via XMP profile could result in a crash [bsc#1271312]
Patch138:         ImageMagick-CVE-2026-61857.patch
# CVE-2026-61861: Use-After-Free in FormatMagickCaption when memory allocation fails [bsc#1271294]
Patch139:         ImageMagick-CVE-2026-61861.patch
# CVE-2026-61870: Memory leak in VIFF encoder when allocation fails [bsc#1271293]
Patch140:         ImageMagick-CVE-2026-61870.patch
# CVE-2026-56372: heap buffer overflow read in magnify operation via unrecognized magnify:method value [bsc#1271314]
Patch141:         ImageMagick-CVE-2026-56372.patch
# CVE-2026-56366: META reader memory leak in the APP1JPEG input path [bsc#1271316]
Patch142:         ImageMagick-CVE-2026-56366.patch
# CVE-2026-61465: Policy Bypass possible with matrix-backed operations [bsc#1271313]
Patch143:         ImageMagick-CVE-2026-61465.patch
# CVE-2026-61858: Policy Bypass in APNG encoder and delegates due to a missing check [bsc#1271311]
Patch144:         ImageMagick-CVE-2026-61858.patch
# CVE-2026-61871: Memory Leak in ICON decoder when allocation fails [bsc#1271485]
Patch145:         ImageMagick-CVE-2026-61871.patch
# CVE-2026-61866: Memory Leak in JNG encoder when a blob could not be opened [bsc#1271489]
Patch146:         ImageMagick-CVE-2026-61866.patch
# CVE-2026-61869: Memory Leak in MIFF encoder when allocation fails [bsc#1271486]
Patch147:         ImageMagick-CVE-2026-61869.patch
# CVE-2026-61867: Memory Leak in TIFF encoder when an allocation fails [bsc#1271488]
Patch148:         ImageMagick-CVE-2026-61867.patch
# CVE-2026-61872: Memory Leak in TIFF encoder when invalid tiff:tile-geometry is specified [bsc#1271484]
Patch149:         ImageMagick-CVE-2026-61872.patch
# CVE-2026-61868: Memory Leak in YUV decoder when opening of blob fails [bsc#1271487]
Patch150:         ImageMagick-CVE-2026-61868.patch
# CVE-2026-61860: Use-After-Free when freetype initialization fails [bsc#1271494]
Patch151:         ImageMagick-CVE-2026-61860.patch
# CVE-2026-56375: Possible memory leak in ASHLAR coder when action fails [bsc#1271495]
Patch152:         ImageMagick-CVE-2026-56375.patch
# CVE-2026-61859: Policy Bypass in script operation due to missing checks [bsc#1271497]
Patch153:         ImageMagick-CVE-2026-61859.patch
# CVE-2026-61865: Memory Leak in hough lines operation when an operation fails [bsc#1271490]
Patch154:         ImageMagick-CVE-2026-61865.patch
# CVE-2026-61864: Memory Leak in color transformation to log colorspace when operation fails [bsc#1271491]
Patch155:         ImageMagick-CVE-2026-61864.patch
# CVE-2026-61464: Heap Buffer Over-Write in X11 import with crafted window title [bsc#1271496]
Patch156:         ImageMagick-CVE-2026-61464.patch
# CVE-2026-61863: Memory Leak in TIFF encoder when a temporary file could not be created [bsc#1271492]
Patch157:         ImageMagick-CVE-2026-61863.patch
# CVE-2026-61862: Information Disclosure when printing profiles with debug enabled [bsc#1271493]
Patch158:         ImageMagick-CVE-2026-61862.patch
# CVE-2026-56365: Memory leak in PNG encoder when writing a MNG image [bsc#1270004]
Patch159:         ImageMagick-CVE-2026-56365.patch
# CVE-2026-62946: Integer Overflow in JNX decoder causes heap buffer over-write when processing extremly large files on 32-bit builds [bsc#1272580]
Patch160:         ImageMagick-CVE-2026-62946.patch
# GHSA-hc76-7mpc-qjqh: Code injection in HTML encoder due to incomplete fix of CVE-2026-25797 [bsc#1272579]
Patch161:         ImageMagick-GHSA-hc76-7mpc-qjqh.patch
# CVE-2026-62363: Heap Buffer Over-Write in fx operation [bsc#1272582]
Patch162:         ImageMagick-CVE-2026-62363.patch
# CVE-2026-62343: Heap Buffer Over-Write in morphology operation when an invalid kernel is provided [bsc#1272575]
Patch163:         ImageMagick-CVE-2026-62343.patch
# CVE-2026-66011: Memory Leak when providing invalid options to the cli [bsc#1272577]
Patch164:         ImageMagick-CVE-2026-66011.patch
# CVE-2026-64685: Heap Buffer Over-Read in BGR decoder due to missing end-of-file check [bsc#1272953]
Patch165:         ImageMagick-CVE-2026-64685.patch
# CVE-2026-86425: Denial of Service due to heap-use-after-free vulnerability [bsc#1279797]
Patch166:         ImageMagick-CVE-2026-86425.patch
# CVE-2026-86423: Denial of service via heap-use-after-free in PerlMagick's GetList method [bsc#1279794]
Patch167:         ImageMagick-CVE-2026-86423.patch
# CVE-2026-86421: Denial of Service via memory leak in MSL decoder [bsc#1279711]
Patch168:         ImageMagick-CVE-2026-86421.patch
# CVE-2026-86420: Denial of Service due to memory budget exhaustion [bsc#1279696]
Patch169:         ImageMagick-CVE-2026-86420.patch
# CVE-2026-93590: failure to perform policy checks during buffer allocation for image pixels in the UHDR encoder can lead to denial of service [bsc#1281913]
Patch170:         ImageMagick-CVE-2026-93590.patch
# CVE-2026-93589: division-by-zero flaw in the FLIF encoder can lead to a denial of service [bsc#1281911]
Patch171:         ImageMagick-CVE-2026-93589.patch
# CVE-2026-93588: NULL pointer dereference in the PNM coder can lead to a denial of service [bsc#1281910]
Patch172:         ImageMagick-CVE-2026-93588.patch
# CVE-2026-93587: policy bypass in the PCD, CUBE and HALD coders can lead to extra memory allocation and a denial of service [bsc#1281909]
Patch173:         ImageMagick-CVE-2026-93587.patch
# CVE-2026-93586: use-after-free in the `ImagesToBlob` method can lead to a process crash [bsc#1281912]
Patch174:         ImageMagick-CVE-2026-93586.patch

BuildRequires:  chrpath
BuildRequires:  dejavu-fonts
BuildRequires:  fdupes
BuildRequires:  gcc-c++
BuildRequires:  libjbig-devel
BuildRequires:  libjpeg-devel
BuildRequires:  libtool
BuildRequires:  libwmf-devel
BuildRequires:  pkgconfig
BuildRequires:  xdg-utils
BuildRequires:  xz-devel
BuildRequires:  zip
BuildRequires:  pkgconfig(OpenEXR)
BuildRequires:  pkgconfig(bzip2)
BuildRequires:  pkgconfig(fftw3)
BuildRequires:  pkgconfig(freetype2)
BuildRequires:  pkgconfig(ijs)
BuildRequires:  pkgconfig(lcms2)
BuildRequires:  pkgconfig(libexif)
BuildRequires:  pkgconfig(libheif)
BuildRequires:  pkgconfig(libopenjp2) >= 2.1.0
BuildRequires:  pkgconfig(libraw)
BuildRequires:  pkgconfig(librsvg-2.0)
BuildRequires:  pkgconfig(libtiff-4) >= 4.0.3
BuildRequires:  pkgconfig(libwebp)
BuildRequires:  pkgconfig(libwebpmux)
BuildRequires:  pkgconfig(libxml-2.0)
BuildRequires:  pkgconfig(lqr-1)
BuildRequires:  pkgconfig(pango)
%if %{with djvu}
BuildRequires:  pkgconfig(ddjvuapi)
%endif
%if 0%{?suse_version} > 1500
BuildRequires:  pkgconfig(libjxl)
%endif
# bsc#1088463
%if %{urw_base35_fonts}
BuildRequires:  urw-base35-fonts
%else
BuildRequires:  ghostscript-fonts-other
BuildRequires:  ghostscript-fonts-std
%endif

%description
ImageMagick is a robust collection of tools and libraries to read,
write, and manipulate an image in many image formats, including popular
formats like TIFF, JPEG, PNG, PDF, PhotoCD, and GIF. With ImageMagick,
you can create images dynamically, making it suitable for Web
applications. You can also resize, rotate, sharpen, color-reduce, or
add special effects to an image and save your completed work in many
different image formats. Image processing operations are available from
the command line as well as through C, C++, and Perl-based programming
interfaces.

# BEGIN NIL FLAVOR
%if "%{flavor}" == ""

%package -n perl-PerlMagick
Summary:        Perl interface for ImageMagick
Group:          Development/Libraries/Perl
Requires:       ImageMagick = %{version}
Requires:       libMagickCore%{libspec}%{clibver} = %{version}
Requires:       perl = %{perl_version}

%description -n perl-PerlMagick
PerlMagick is an objected-oriented Perl interface to ImageMagick. Use
the module to read, manipulate, or write an image or image sequence
from within a Perl script. This makes it suitable for Web CGI scripts.

%package devel
Summary:        Development files for ImageMagick's C interface
Group:          Development/Libraries/C and C++
Requires:       ImageMagick = %{version}
Requires:       glibc-devel
Requires:       libMagickCore%{libspec}%{clibver} = %{version}
Requires:       libMagickWand%{libspec}%{cwandver} = %{version}
# bnc#741947:
Requires:       pkgconfig(bzip2)

%description devel
ImageMagick is a robust collection of tools and libraries to read,
write, and manipulate an image in many image formats, including popular
formats like TIFF, JPEG, PNG, PDF, PhotoCD, and GIF. With ImageMagick,
you can create images dynamically, making it suitable for Web
applications. You can also resize, rotate, sharpen, color-reduce, or
add special effects to an image and save your completed work in many
different image formats. Image processing operations are available from
the command line as well as through C, C++, and Perl-based programming
interfaces.

%if !%{debug_build}
%package extra
Summary:        Extra codecs for the ImageMagick image viewer/converter
Group:          Productivity/Graphics/Other
Requires:       ImageMagick = %{version}
Requires:       libMagickCore%{libspec}%{clibver} = %{version}
Recommends:     autotrace
Recommends:     dcraw
Recommends:     hp2xx
Recommends:     libwmf
Recommends:     netpbm
Recommends:     transfig

%description extra
This package adds support for djvu, wmf and jpeg2000 formats and
installs optional helper applications.
%endif

%package -n libMagickCore%{libspec}%{clibver}
Summary:        C runtime library for ImageMagick
Group:          Productivity/Graphics/Other
Requires:       imagick-config-7
Recommends:     ImageMagick-config-7-SUSE
Recommends:     ghostscript
Suggests:       ImageMagick-extra = %{version}
Recommends:     ImageMagick

%description -n libMagickCore%{libspec}%{clibver}
ImageMagick is a robust collection of tools and libraries to read,
write, and manipulate an image in many image formats, including popular
formats like TIFF, JPEG, PNG, PDF, PhotoCD, and GIF. With ImageMagick,
you can create images dynamically, making it suitable for Web
applications. You can also resize, rotate, sharpen, color-reduce, or
add special effects to an image and save your completed work in many
different image formats. Image processing operations are available from
the command line as well as through C, C++, and Perl-based programming
interfaces.

%package -n libMagickWand%{libspec}%{cwandver}
Summary:        C runtime library for ImageMagick
Group:          Productivity/Graphics/Other
Recommends:     ImageMagick

%description -n libMagickWand%{libspec}%{cwandver}
ImageMagick is a robust collection of tools and libraries to read,
write, and manipulate an image in many image formats, including popular
formats like TIFF, JPEG, PNG, PDF, PhotoCD, and GIF. With ImageMagick,
you can create images dynamically, making it suitable for Web
applications. You can also resize, rotate, sharpen, color-reduce, or
add special effects to an image and save your completed work in many
different image formats. Image processing operations are available from
the command line as well as through C, C++, and Perl-based programming
interfaces.

%package -n libMagick++%{libspec}%{cxxlibver}
Summary:        C++ interface runtime library for ImageMagick
Group:          Development/Libraries/C and C++
Recommends:     ImageMagick

%description -n libMagick++%{libspec}%{cxxlibver}
This is Magick++, the object-oriented C++ API for the ImageMagick
image-processing library.

Magick++ supports an object model inspired by PerlMagick. Magick++
should be faster than PerlMagick since it is written in a compiled
language which is not parsed at run-time. This makes it suitable for
Web CGI programs. Images support implicit reference counting so that
copy constructors and assignment incur almost no cost. The cost of
actually copying an image (if necessary) is done just before
modification and this copy is managed automatically by Magick++.
De-referenced copies are automatically deleted. The image objects
support value (rather than pointer) semantics so it is trivial to
support multiple generations of an image in memory at one time.

%package -n libMagick++-devel
Summary:        Development files for ImageMagick's C++ interface
Group:          Development/Libraries/C and C++
Requires:       libMagick++%{libspec}%{cxxlibver} = %{version}
Requires:       libstdc++-devel
Requires:       pkgconfig(ImageMagick) = %{mfr_version}

%description -n libMagick++-devel
This is Magick++, the object-oriented C++ API for the ImageMagick
image-processing library.

Magick++ supports an object model inspired by PerlMagick. Magick++
should be faster than PerlMagick since it is written in a compiled
language which is not parsed at run-time. This makes it suitable for
Web CGI programs. Images support implicit reference counting so that
copy constructors and assignment incur almost no cost. The cost of
actually copying an image (if necessary) is done just before
modification and this copy is managed automatically by Magick++.
De-referenced copies are automatically deleted. The image objects
support value (rather than pointer) semantics so it is trivial to
support multiple generations of an image in memory at one time.

%package doc
Summary:        Document Files for ImageMagick Library
Group:          Documentation/HTML
BuildArch:      noarch

%description doc
HTML documentation for ImageMagick library and scene examples.

%package config-7-SUSE
Summary:        SUSE Provided Configuration
Group:          Development/Libraries/C and C++
Provides:       imagick-config-7
Conflicts:      imagick-config-7
BuildArch:      noarch

%description config-7-SUSE
ImageMagick configuration as provided by SUSE. It is upstream 'secure'
policy plus disable few other coders for reading and/or writing.

%prep
%autosetup -p1 -n ImageMagick-%{source_version}

%build
# bsc#1088463
%if %{urw_base35_fonts}
sed -i 's:type1:otf:'      config/type-urw-base35.xml.in
sed -i 's:metrics=[^ ]*::' config/type-urw-base35.xml.in
sed -i 's:\.t1:.otf:'      config/type-urw-base35.xml.in
%endif
# Legal-Review-Notice: removed because of proprietary license
rm images/logo.eps
# make library binary package parallel installable
export MODULES_DIRNAME="modules%{libspec}%{clibver}"
export SHAREARCH_DIRNAME="config%{libspec}%{clibver}"
%if %{debug_build}
export CFLAGS="%{optflags} -O0"
export CXXFLAGS="%{optflags} -O0"
%endif
export CONFIGURE_RELATIVE_PATH=%{config_dir}
%configure \
  --disable-silent-rules \
  --enable-shared \
  --without-frozenpaths \
  --with-magick_plus_plus \
%if !%{debug_build}
  --with-modules \
%else
  --without-modules \
%endif
  --with-threads \
%if %{urw_base35_fonts}
  --with-urw-base35-font-dir=%{_datadir}/fonts/truetype \
%else
  --with-gs-font-dir=%{_datadir}/fonts/ghostscript \
%endif
  --with-perl \
  --with-perl-options="INSTALLDIRS=vendor INSTALLVENDORARCH=%{perl_vendorarch} INSTALLVENDORMAN3DIR=/usr/share/man/man3" \
  --disable-static \
  --with-gvc \
%if %{with ddjvuapi}
  --with-djvu \
%endif
  --with-fftw \
  --with-lcms \
  --with-jbig \
  --with-openjp2 \
  --with-openexr \
  --with-rsvg \
  --with-webp \
  --with-wmf \
  --with-quantum-depth=%{quantum_depth} \
  --without-gcc-arch \
  --enable-pipes=no \
  --enable-reproducible-build=yes \
  --disable-openmp \
  --disable-dpc

%if %{asan_build}
sed -i -e 's/\(^CFLAGS.*\)/\1 -fsanitize=address/' \
       -e 's/\(^LIBS =.*\)/\1 -lasan/' \
       Makefile
%endif
# don't build together, PerlMagick could be miscompiled when using parallel build[1]
# [1] http://pkgs.fedoraproject.org/cgit/ImageMagick.git/tree/ImageMagick.spec
%make_build all
%make_build -j1 perl-build
# mostly because */demo is used later with %%check
# polutting dir with .libs etc.
cp -r Magick++/demo Magick++/examples
cp -r PerlMagick/demo PerlMagick/examples
# other improvements
chmod -x PerlMagick/demo/*.pl
exit 0

%check
%ifarch i586
# do not report test issues related to 32-bit architectures upstream,
# they do not want to dedicate any time to fix them:
# https://github.com/ImageMagick/ImageMagick/issues/1215
exit 0
%endif
%if %{debug_build} || %{asan_build}
# testsuite does not succeed for some reason
# research TODO
exit 0
%endif
# ensure we do not block any coder by security policy
cp config/policy-open.xml config/policy.xml
%make_build check
export MAGICK_CODER_MODULE_PATH=$PWD/coders/.libs
export MAGICK_CODER_FILTER_PATH=$PWD/filters/.libs
export MAGICK_CONFIGURE_PATH=$PWD/config
cd PerlMagick
%if %{test_verbose}
sed -i 's:TEST_VERBOSE=0:TEST_VERBOSE=1:' Makefile
%endif
%make_build test
cd ..

%install
%make_install pkgdocdir=%{_defaultdocdir}/ImageMagick-7/
cp %{SOURCE4} %{buildroot}%{_sysconfdir}/%{config_dir}/policy.xml
# symlink header file relative to /usr/include/ImageMagick-7/
# so that inclusions like wand/*.h and magick/*.h work
ln -s ./MagickCore %{buildroot}%{_includedir}/ImageMagick-7/magick
ln -s ./MagickWand %{buildroot}%{_includedir}/ImageMagick-7/wand
# these will be included via %%doc
rm -r %{buildroot}%{_datadir}/doc/ImageMagick-7/
rm %{buildroot}%{_libdir}/*.la
# remove RPATH from perl module
perl_module=$(find %{buildroot}%{_prefix}/lib/perl5 -name '*.so')
chmod 755 $perl_module
chrpath -d $perl_module
chmod 555 $perl_module
# remove %%{buildroot} from distributed file
sed -i 's:%{buildroot}::' %{buildroot}/%{_libdir}/ImageMagick-%{mfr_version}/config%{libspec}%{clibver}/configure.xml
#remove duplicates
%fdupes -s %{buildroot}%{_defaultdocdir}/ImageMagick-7
%fdupes -s %{buildroot}%{_includedir}/ImageMagick-7
%fdupes -s %{buildroot}%{_libdir}/pkgconfig
%perl_process_packlist

%post -n libMagickCore%{libspec}%{clibver} -p /sbin/ldconfig
%postun -n libMagickCore%{libspec}%{clibver} -p /sbin/ldconfig
%post -n libMagickWand%{libspec}%{cwandver} -p /sbin/ldconfig
%postun -n libMagickWand%{libspec}%{cwandver} -p /sbin/ldconfig
%post -n libMagick++%{libspec}%{cxxlibver} -p /sbin/ldconfig
%postun -n libMagick++%{libspec}%{cxxlibver} -p /sbin/ldconfig

%files
%license LICENSE
%{_bindir}/[^MW]*
%{_mandir}/man1/*
%exclude %{_mandir}/man1/*-config.1%{ext_man}
%{_datadir}/ImageMagick-7
%{_sysconfdir}/%{config_dir}
%exclude %{_sysconfdir}/%{config_dir}/policy.xml

%files -n libMagickCore%{libspec}%{clibver}
%license LICENSE
%{_libdir}/libMagickCore*.so.%{clibver}*
%dir %{_libdir}/ImageMagick*
%if !%{debug_build}
%dir %{_libdir}/ImageMagick*/modules*
%dir %{_libdir}/ImageMagick*/modules*/*
%exclude %{_libdir}/ImageMagick*/modules*/*/wmf.*
%exclude %{_libdir}/ImageMagick*/modules*/*/jp2.*
%if %{with djvu}
%exclude %{_libdir}/ImageMagick*/modules*/*/djvu.*
%endif
%{_libdir}/ImageMagick*/modules*/*/*.so
# don't remove la files, see bnc#579798
%{_libdir}/ImageMagick*/modules*/*/*.la
%endif
%{_libdir}/ImageMagick*/config*

%files -n libMagickWand%{libspec}%{cwandver}
%{_libdir}/libMagickWand*.so.%{cwandver}*

%if !%{debug_build}
%files extra
%{_libdir}/ImageMagick*/modules*/*/wmf.so
# don't remove la files, see bnc#579798
%{_libdir}/ImageMagick*/modules*/*/jp2.so
%{_libdir}/ImageMagick*/modules*/*/jp2.la
%if %{with djvu}
%{_libdir}/ImageMagick*/modules*/*/djvu.so
%{_libdir}/ImageMagick*/modules*/*/djvu.la
%endif
%endif

%files devel
%{_libdir}/libMagickCore*.so
%{_libdir}/libMagickWand*.so
%dir %{_includedir}/ImageMagick*
%{_includedir}/ImageMagick*/MagickCore
%{_includedir}/ImageMagick*/MagickWand
%{_includedir}/ImageMagick*/magick
%{_includedir}/ImageMagick*/wand
%{_bindir}/MagickCore-config
%{_bindir}/MagickWand-config
%{_libdir}/pkgconfig/MagickCore*.pc
%{_libdir}/pkgconfig/ImageMagick*.pc
%{_libdir}/pkgconfig/MagickWand*.pc
%{_mandir}/man1/*-config.1%{?ext_man}
%exclude %{_mandir}/man1/Magick++-config.1%{ext_man}

%files -n perl-PerlMagick
%doc PerlMagick/README.txt
%doc PerlMagick/examples
%{_mandir}/man3/*
%{perl_vendorarch}/auto/Image
%{perl_vendorarch}/Image

%files -n libMagick++%{libspec}%{cxxlibver}
%{_libdir}/libMagick++*.so.%{cxxlibver}*

%files -n libMagick++-devel
%doc Magick++/examples
%doc Magick++/AUTHORS
%{_libdir}/libMagick++*.so
%{_includedir}/ImageMagick*/Magick++.h
%{_includedir}/ImageMagick*/Magick++
%{_bindir}/Magick++-config
%{_libdir}/pkgconfig/Magick++*.pc
%{_mandir}/man1/Magick++-config.1%{?ext_man}

%files doc
%{_defaultdocdir}/ImageMagick-7

%files config-7-SUSE
%{_sysconfdir}/%{config_dir}/policy.xml

%endif
# END NIL FLAVOR

%if "%{flavor}" == "config_open"
%package config-7-upstream-open
Summary:        Open ImageMagick Security Policy
Group:          Development/Libraries/C and C++
Provides:       imagick-config-7
Obsoletes:      config-7-upstream < %{version}
Provides:       config-7-upstream = %{version}
Conflicts:      imagick-config-7
BuildArch:      noarch

%description config-7-upstream-open
This policy is designed for usage in secure settings like those
protected by firewalls or within Docker containers. Within this framework,
ImageMagick enjoys broad access to resources and functionalities. This policy
provides convenient and adaptable options for image manipulation. However,
it's important to note that it might present security vulnerabilities in
less regulated conditions. Thus, organizations should thoroughly assess
the appropriateness of the open policy according to their particular use
case and security prerequisites.

%prep
%setup -q -n ImageMagick-%{source_version}
%patch -P 0 -p1

%build

%install
mkdir -p %{buildroot}%{_sysconfdir}/%{config_dir}/
cp config/policy-open.xml %{buildroot}%{_sysconfdir}/%{config_dir}/policy.xml

%files config-7-upstream-open
%dir %{_sysconfdir}/%{config_dir}
%config(noreplace) %{_sysconfdir}/%{config_dir}/policy.xml
%endif

%if "%{flavor}" == "config_limited"
%package config-7-upstream-limited
Summary:        Limited ImageMagick Security Policy
Group:          Development/Libraries/C and C++
Provides:       imagick-config-7
Conflicts:      imagick-config-7
BuildArch:      noarch

%description config-7-upstream-limited
The primary objective of the limited security policy is to find a
middle ground between convenience and security. This policy involves the
deactivation of potentially hazardous functionalities, like specific coders
such as SVG or HTTP. Furthermore, it establishes several constraints on
the utilization of resources like memory, storage, and processing duration,
all of which are adjustable. This policy proves advantageous in situations
where there's a need to mitigate the potential threat of handling possibly
malicious or demanding images, all while retaining essential capabilities
for prevalent image formats.

%prep
%setup -q -n ImageMagick-%{source_version}
%patch -P 0 -p1

%build

%install
mkdir -p %{buildroot}%{_sysconfdir}/%{config_dir}/
cp config/policy-limited.xml %{buildroot}%{_sysconfdir}/%{config_dir}/policy.xml

%files config-7-upstream-limited
%dir %{_sysconfdir}/%{config_dir}
%config(noreplace) %{_sysconfdir}/%{config_dir}/policy.xml
%endif

%if "%{flavor}" == "config_secure"
%package config-7-upstream-secure
Summary:        Secure ImageMagick Security Policy
Group:          Development/Libraries/C and C++
Provides:       imagick-config-7
Conflicts:      imagick-config-7
BuildArch:      noarch

%description config-7-upstream-secure
This stringent security policy prioritizes the implementation of
rigorous controls and restricted resource utilization to establish a
profoundly secure setting while employing ImageMagick. It deactivates
conceivably hazardous functionalities, including specific coders like
SVG or HTTP. The policy promotes the tailoring of security measures to
harmonize with the requirements of the local environment and the guidelines
of the organization. This protocol encompasses explicit particulars like
limitations on memory consumption, sanctioned pathways for reading and
writing, confines on image sequences, the utmost permissible duration of
workflows, allocation of disk space intended for image data, and even an
undisclosed passphrase for remote connections. By adopting this robust
policy, entities can elevate their overall security stance and alleviate
potential vulnerabilities.

%prep
%setup -q -n ImageMagick-%{source_version}
%patch -P 0 -p1

%build

%install
mkdir -p %{buildroot}%{_sysconfdir}/%{config_dir}/
cp config/policy-secure.xml %{buildroot}%{_sysconfdir}/%{config_dir}/policy.xml

%files config-7-upstream-secure
%dir %{_sysconfdir}/%{config_dir}
%config(noreplace) %{_sysconfdir}/%{config_dir}/policy.xml
%endif

%if "%{flavor}" == "config_websafe"
%package config-7-upstream-websafe
Summary:        Web-safe ImageMagick Security Policy
Group:          Development/Libraries/C and C++
Provides:       imagick-config-7
Conflicts:      imagick-config-7
BuildArch:      noarch

%description config-7-upstream-websafe
This security protocol designed for web-safe usage focuses on situations
where ImageMagick is applied in publicly accessible contexts, like websites.
It deactivates the capability to read from or write to any image formats
other than web-safe formats like GIF, JPEG, and PNG. Additionally, this
policy prohibits the execution of image filters and indirect reads, thereby
thwarting potential security breaches. By implementing these limitations,
the web-safe policy fortifies the safeguarding of systems accessible to
the public, reducing the risk of exploiting ImageMagick's capabilities
for potential attacks.

%prep
%setup -q -n ImageMagick-%{source_version}
%patch -P 0 -p1

%build

%install
mkdir -p %{buildroot}%{_sysconfdir}/%{config_dir}/
cp config/policy-websafe.xml %{buildroot}%{_sysconfdir}/%{config_dir}/policy.xml

%files config-7-upstream-websafe
%dir %{_sysconfdir}/%{config_dir}
%config(noreplace) %{_sysconfdir}/%{config_dir}/policy.xml
%endif

%changelog
