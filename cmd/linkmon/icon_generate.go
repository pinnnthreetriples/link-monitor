package main

//go:generate go run ../../tools/iconasset icon.ico
//go:generate go run github.com/tc-hib/go-winres@v0.3.3 simply --arch amd64 --out rsrc --icon icon.ico --manifest none --file-description "Link Monitor" --product-name "Link Monitor" --original-filename linkmon.exe
