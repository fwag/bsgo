#
# spec file for package openSUSE-ALP-dummy-release.spec
#
# Copyright (c) 2020 SUSE LLC
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


%define product ALP
%define betaversion %{nil}
%define codename %{nil}
Name:           ALP-dummy-release
Version:        0.1
Release:        0
# 0 is the product release, not the build release of this package
Summary:        Adaptable Linux Platform 
License:        GPL-2.0-or-later
Group:          System/Fhs
Source100:      weakremovers.inc
Source1:	COPYING
Suggests:       branding-openSUSE
Suggests:       distribution-logos-openSUSE-ALP
Suggests:       java-11-openjdk
Suggests:       mariadb
Suggests:       mariadb-client
Suggests:       openSUSE-build-key
Suggests:       openssl-1_1
Conflicts:      core-release <= 10
Conflicts:      distribution-release
Provides:       distribution-release
# ALP is a SUSE Linux type distribution
Provides:       suse-release = %{version}-%{release}
Provides:       suse-release-oss = %{version}-%{release}
# Build using microos patterns
Provides:	MicroOS-release
Obsoletes:	MicroOS-release
# Give zypp a hint that this product must be kept up-to-date using zypper dup, not up (boo#1061384)
Provides:       product-update() = dup
# With more than one product in the FTP tree, yast needs to know which products are installable
# The name is referenced by the control file as well
Provides:       system-installation() = ALP

# this package should only be available for the "basearchs" of a product
ExclusiveArch:  %ix86 x86_64 ppc64le riscv64 s390x aarch64 %arm
%include %{SOURCE100}
Provides:       %name-%version
Provides:       product() = ALP
Provides:       product(ALP) = 0.1-0
Provides:       product-label() = ALP
Provides:       product-cpeid() = cpe%3A%2Fo%3Asuse%3Aalp%3A0.1
Provides:       product-url(repository) = http%3A%2F%2Fdownload.opensuse.org%2Frepositories%2FSUSE%3A%2FALP%3A%2Fstandard%2F
Provides:       product-endoflife()


%description




%prep
%setup -qcT
mkdir license

%build

%install
mkdir -p %{buildroot}%{_sysconfdir} %{buildroot}%{_prefix}/lib/issue.d %{buildroot}/run

echo -e 'Welcome to %{product} %{codename} %{version}%{?betaversion: %{betaversion}} - Kernel \\r (\\l).\n' > %{buildroot}%{_prefix}/lib/issue.d/10-ALP.conf
echo -e "This is an early sneak preview release  not intended for production environment.\n" > %{buildroot}%{_prefix}/lib/issue.d/90-ALP.conf
echo    'Welcome to %{product} %{codename} %{version}%{?betaversion: %{betaversion}} - Kernel %%r (%%t).' > %{buildroot}%{_sysconfdir}/issue.net

VERSION_ID=`echo %{version}|tr '[:upper:]' '[:lower:]'|sed -e 's/ //g;'`
# note: VERSION is an optional field and has no meaning other than informative on a rolling distro
# We do thus not add it to the os-release file
cat > %{buildroot}%{_prefix}/lib/os-release <<EOF
NAME="ALP"
# VERSION="%{version}%{?betaversion: %{betaversion}}"
ID="alp"
ID_LIKE="suse opensuse opensuse-tumbleweed"
VERSION_ID="$VERSION_ID"
PRETTY_NAME="ALP"
ANSI_COLOR="0;32"
CPE_NAME="cpe:/o:suse:alp:%{version}"
BUG_REPORT_URL="https://bugs.opensuse.org"
HOME_URL="https://www.opensuse.org/"
# DOCUMENTATION_URL="https://en.opensuse.org/Portal:ALP"
LOGO="distributor-logo"
EOF
ln -s ..%{_prefix}/lib/os-release %{buildroot}%{_sysconfdir}/os-release

mkdir -p %{buildroot}%{_prefix}/lib/motd.d/
echo "Have a lot of fun..." > %{buildroot}%{_prefix}/lib/motd.d/welcome
# Bug 404141 - /etc/YaST/control.xml should be owned by some package
mkdir -p %{buildroot}%{_sysconfdir}/YaST2/
echo %{buildroot}
touch %{buildroot}%{_sysconfdir}/YaST2/control.xml

# enable vendor change openSUSE,SUSE for DUP from 15.3 to TW (boo#1198332)
mkdir -p %{buildroot}%{_sysconfdir}/zypp/vendors.d
echo -e "[main]\nvendors=openSUSE,SUSE,SUSE LLC <https://www.suse.com/>\n" > %{buildroot}%{_sysconfdir}/zypp/vendors.d/00-openSUSE.conf


# fate#319341, make openSUSE-release own YaST license files. TODO:
# get rid of /etc/YaST2/licenses
install -D -d -m 755 "%{buildroot}%_defaultlicensedir/product/base"
install -D -d -m 755 "%{buildroot}%_defaultlicensedir"
cp -a license "%{buildroot}%_defaultlicensedir/%name"
pushd license
for i in *; do
  mkdir %{buildroot}%_defaultlicensedir/product/base/$i
done

mkdir -p %{buildroot}%{_sysconfdir}/products.d
cat >%{buildroot}%{_sysconfdir}/products.d/ALP.prod << EOF
<?xml version="1.0" encoding="UTF-8"?>
<product schemeversion="0">
  <vendor>SUSE</vendor>
  <name>ALP</name>
  <version>0.1</version>
  <release>0</release>
  <endoflife></endoflife>
  <arch>%{_target_cpu}</arch>
  <cpeid>cpe:/o:suse:alp:0.1</cpeid>
  <productline>ALP</productline>
  <register>
    <pool>
    </pool>
    <updates>
    </updates>
  </register>
  <repositories>
  </repositories>
  <summary>Adaptable Linux Platform</summary>
  <shortsummary>ALP</shortsummary>
  <description/>
  <linguas>
    <language>en_US</language>
  </linguas>
  <urls>
    <url name="repository">http://download.opensuse.org/repositories/SUSE:/ALP:/standard/</url>
  </urls>
  <buildconfig>
    <producttheme>ALP</producttheme>
    <create_flavors>false</create_flavors>
  </buildconfig>
  <installconfig>
    <defaultlang>en_US</defaultlang>
    <datadir>suse</datadir>
    <descriptiondir>suse/setup/descr</descriptiondir>
    <default_obs_repository_name>openSUSE_Tumbleweed</default_obs_repository_name>
    <default_obs_download_url>%{_download_url}</default_obs_download_url>
    <releasepackage name="%{name}" flag="EQ" version="%{version}" release="%{release}"/>
    <distribution>SUSE</distribution>
  </installconfig>
  <runtimeconfig/>
</product>

EOF


# this is a base product, create symlink
ln -s ALP.prod %{buildroot}%{_sysconfdir}/products.d/baseproduct

mkdir -p "%{buildroot}/usr/share/licenses/ALP-dummy-release"
install -D -m0644 "%{SOURCE1}" "%{buildroot}/usr/share/licenses/ALP-dummy-release/"


%posttrans
# Launch the issue-generator: we have a new config file in /usr/lib/issue.d that needs to be represented
if [ -x %{_sbindir}/issue-generator ]; then
    if [ -x %{_bindir}/systemd-tmpfiles ]; then
      %{_bindir}/systemd-tmpfiles --create issue-generator.conf || :
    fi
    %{_sbindir}/issue-generator || :
fi

%files
%dir /usr/share/licenses/ALP-dummy-release
%license /usr/share/licenses/ALP-dummy-release/COPYING
%defattr(644,root,root,755)
%dir %_defaultlicensedir/product
%_defaultlicensedir/product/base
%license license
%{_sysconfdir}/os-release
%{_prefix}/lib/os-release
# Bug 404141 - /etc/YaST/control.xml should be owned by some package
%dir %{_sysconfdir}/YaST2/
%config %{_sysconfdir}/YaST2/control.xml
%dir %{_sysconfdir}/zypp
%dir %{_sysconfdir}/zypp/vendors.d
%config %{_sysconfdir}/zypp/vendors.d/00-openSUSE.conf
%{_prefix}/lib/motd.d/welcome
%dir %{_prefix}/lib/issue.d/
%{_prefix}/lib/issue.d/10-ALP.conf
%{_prefix}/lib/issue.d/90-ALP.conf
%config(noreplace) %{_sysconfdir}/issue.net
%{_sysconfdir}/products.d

%changelog
