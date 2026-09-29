%global debug_package %{nil}
%global __strip /bin/true
Name:           mountmgr-cli
Version:        @VERSION@
Release:        1%{?dist}
Summary:        Mount Manager admin CLI
License:        Proprietary

Source0:        %{name}-%{version}.tar.gz

%description
mmctl: command line administration for Mount Manager (templates, groups, hosts, tokens, audit).

%prep
%autosetup

%install
install -D -m 0755 mmctl %{buildroot}%{_bindir}/mmctl

%files
%{_bindir}/mmctl

%changelog
@CHANGELOG@
