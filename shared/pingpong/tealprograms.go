// Copyright (C) 2019 Algorand, Inc.
// This file is part of go-algorand
//
// go-algorand is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, either version 3 of the
// License, or (at your option) any later version.
//
// go-algorand is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with go-algorand.  If not, see <https://www.gnu.org/licenses/>.

package pingpong

import "fmt"

// This teal program will be evaluated to true
var airdropTeal = "int 1"

// Time locked hash contract teal script
// The hash secret is YldbVnM3AMyRMx/xkowYf2w9JKdDBXIRZGFz3bx8tuQ= (base64)
// Note: this contract didn't specify closed to account intentionally
func tlhc(sender string, to string) string {
	template := `
txn Receiver
addr %[2]s
==
arg 0
len
int 32
==
&&
arg 0
sha256
byte base64 iZWMx72KvU6Bw6sPAWQFL96YH+VMrBA0XKWD9XbZOZI=
==
&&
txn Receiver
addr %[1]s
==
global Round
int 0
>
&&
||
txn Fee
int 1000000
<
&&
`
	return fmt.Sprintf(template, sender, to)
}

//TODO: dirty teal
func dirtyTeal() string {
	return "int 0"
}