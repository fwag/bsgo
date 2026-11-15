Name: hello
Version: 2.12
Release: 3.1
Summary: demo app
License: MIT
BuildRequires: pkgconfig(greet) >= 1.0
Requires: libgreet.so.1()(64bit)
%description
demo
%install
mkdir -p %{buildroot}/usr/bin
echo hi > %{buildroot}/usr/bin/hello
%files
/usr/bin/hello
