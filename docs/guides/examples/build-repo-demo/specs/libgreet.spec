Name: libgreet
Version: 1.0
Release: 1.1
Summary: demo library
License: MIT
%description
demo
%package -n libgreet1
Summary: runtime
Provides: libgreet.so.1()(64bit)
%description -n libgreet1
runtime
%package devel
Summary: devel
Requires: libgreet1 = %{version}
Provides: pkgconfig(greet) = 1.0
%description devel
devel
%install
mkdir -p %{buildroot}/usr/lib64 %{buildroot}/usr/bin %{buildroot}/usr/share/pkgconfig
echo lib > %{buildroot}/usr/lib64/libgreet.so.1
echo cfg > %{buildroot}/usr/bin/greet-config
echo pc > %{buildroot}/usr/share/pkgconfig/greet.pc
%files -n libgreet1
/usr/lib64/libgreet.so.1
%files devel
/usr/bin/greet-config
/usr/share/pkgconfig/greet.pc
