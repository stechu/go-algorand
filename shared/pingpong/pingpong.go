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

import (
	"context"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"math"
	"math/rand"
	"os"
	"time"

	"github.com/algorand/go-algorand/data/transactions"
	"github.com/algorand/go-algorand/data/transactions/logic"
	"github.com/algorand/go-algorand/crypto"
	"github.com/algorand/go-algorand/data/basics"
	"github.com/algorand/go-algorand/libgoal"
)

// PrepareAccounts to set up accounts required for Ping Pong run
func PrepareAccounts(ac libgoal.Client, initCfg PpConfig) (accounts map[string]uint64, cfg PpConfig, err error) {
	cfg = initCfg
	accounts, cfg, err = ensureAccounts(ac, cfg)
	if err != nil {
		return
	}

	err = fundAccounts(accounts, ac, cfg)
	if err != nil {
		return
	}

	return
}

func fundAccounts(accounts map[string]uint64, client libgoal.Client, cfg PpConfig) error {
	srcFunds := accounts[cfg.SrcAccount]

	// Fee of 0 will make cause the function to use the suggested one by network
	fee := uint64(0)

	for addr, balance := range accounts {
		if balance < cfg.MinAccountFunds {
			toSend := cfg.MinAccountFunds - balance
			if srcFunds <= toSend {
				return fmt.Errorf("source account has insufficient funds %d - needs %d", srcFunds, toSend)
			}
			srcFunds -= toSend
			_, err := client.SendPaymentFromUnencryptedWallet(cfg.SrcAccount, addr, fee, toSend, nil)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func refreshAccounts(accounts map[string]uint64, client libgoal.Client, cfg PpConfig) error {
	for addr := range accounts {
		amount, err := client.GetBalance(addr)
		if err != nil {
			return err
		}

		accounts[addr] = amount
	}

	return fundAccounts(accounts, client, cfg)
}

// return a shuffled list of accounts with some minimum balance
func listSufficientAccounts(accounts map[string]uint64, minimumAmount uint64, except string) []string {
	out := make([]string, 0, len(accounts))
	for key, value := range accounts {
		if key == except {
			continue
		}
		if value >= minimumAmount {
			out = append(out, key)
		}
	}
	rand.Shuffle(len(out), func(i, j int) { t := out[i]; out[i] = out[j]; out[j] = t })
	return out
}

// RunPingPong starts ping pong process
func RunPingPong(ctx context.Context, ac libgoal.Client, accounts map[string]uint64, cfg PpConfig) {
	// Infinite loop given:
	//  - accounts -> map of accounts to include in transfers (including src account, which we don't want to use)
	//  - cfg      -> configuration for how to proceed
	// LOOP {
	// 		for time.Now() < StopRunTime
	//			FromList = Randomize list of accounts
	//			ToList = Randomize list of accounts
	//			for i, from := range FromList
	//				Send(from, ToList[i], CalcAmount, CalcFee)
	//			If DelayBetween != 0 { sleep(delay) }
	//		If RestTime > 0 { sleep(RestTime) }
	//		If time-to-refresh
	//			accounts, cfg, err = PrepareAccounts()
	//			error = fundAccounts()
	//  }


	var fromList, toList, addrs []string
	var programs [][]byte
	var err error

	if cfg.TLHC || cfg.DirtyTeal || cfg.Airdrop {
		fromList = listSufficientAccounts(accounts, 0, cfg.SrcAccount)
		if cfg.TLHC {
			toList = listSufficientAccounts(accounts, 0, cfg.SrcAccount)
			programs, addrs, err = generateTLHC(fromList, toList)
		} else if cfg.DirtyTeal {
			programs, addrs, err = generateDirtyTeal(len(fromList))
		} else if cfg.Airdrop {
			programs, addrs, err = generateAirdrop(len(fromList))
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error generating contract: %v\n", err)
			return
		}
		err = refreshContractAccount(addrs, ac, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error refreshing tlhc accounts: %v\n", err)
		}
	}

	var runTime time.Duration
	if cfg.RunTime > 0 {
		runTime = cfg.RunTime
	} else {
		runTime = 10000 * time.Hour // Effectively 'forever'
	}
	restTime := cfg.RestTime
	refreshTime := time.Now().Add(cfg.RefreshTime)

	for {
		if ctx.Err() != nil {
			break
		}
		startTime := time.Now()
		stopTime := startTime.Add(runTime)

		var totalSent, totalSucceeded uint64
		for !time.Now().After(stopTime) {
			var sent, succeeded uint64
			var err error
			if cfg.TLHC || cfg.DirtyTeal || cfg.Airdrop {
				sent, succeeded, err = contractWithdraw(addrs, fromList, programs, ac, cfg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error withdraw tlhc txn: %v\n", err)
 				}

			} else {
				fromList = listSufficientAccounts(accounts, (cfg.MaxAmt+cfg.MaxFee)*2, cfg.SrcAccount)
				toList = listSufficientAccounts(accounts, 0, cfg.SrcAccount)
				sent, succeeded, err = sendFromTo(fromList, toList, ac, cfg)
			}
			totalSent += sent
			totalSucceeded += succeeded
			if err != nil {
				fmt.Fprintf(os.Stderr, "error sending transactions: %v\n", err)
			}

			if cfg.RefreshTime > 0 && time.Now().After(refreshTime) {
				if cfg.TLHC || cfg.DirtyTeal || cfg.Airdrop { //refresh contracts
					fromList = listSufficientAccounts(accounts, 0, cfg.SrcAccount)
					if cfg.TLHC {
						toList = listSufficientAccounts(accounts, 0, cfg.SrcAccount)
						programs, addrs, err = generateTLHC(fromList, toList)
					} else if cfg.DirtyTeal {
						programs, addrs, err = generateDirtyTeal(len(fromList))
					} else if cfg.Airdrop {
						programs, addrs, err = generateAirdrop(len(fromList))
					}

					if err != nil {
						fmt.Fprintf(os.Stderr, "error generating txn: %v\n", err)
						return
					}
					err = refreshContractAccount(addrs, ac, cfg)
					if err != nil {
						fmt.Fprintf(os.Stderr, "error refreshing: %v\n", err)
					}
				} else { //refresh non-contract accounts
					err = refreshAccounts(accounts, ac, cfg)
					if err != nil {
						fmt.Fprintf(os.Stderr, "error refreshing: %v\n", err)
					}
				}
				refreshTime = refreshTime.Add(cfg.RefreshTime)
			}
		}
		timeDelta := time.Now().Sub(startTime)
		fmt.Fprintf(os.Stdout, "Sent %d transactions (%d attempted) in %d seconds\n", totalSucceeded, totalSent, int(math.Round(timeDelta.Seconds())))
		if cfg.RestTime > 0 {
			fmt.Fprintf(os.Stdout, "Pausing %d seconds before sending more transactions\n", int(math.Round(cfg.RestTime.Seconds())))
			time.Sleep(restTime)
		}
	}
}

func sendFromTo(fromList, toList []string, client libgoal.Client, cfg PpConfig) (sentCount, successCount uint64, err error) {
	amt := cfg.MaxAmt
	fee := cfg.MaxFee

	for i, from := range fromList {
		if cfg.RandomizeAmt {
			amt = rand.Uint64()%cfg.MaxAmt + 1
		}

		if cfg.RandomizeFee {
			fee = rand.Uint64()%(cfg.MaxFee-cfg.MinFee) + cfg.MinFee
		}

		if !cfg.Quiet {
			fmt.Fprintf(os.Stdout, "Sending %d : %s -> %s\n", amt, from, toList[i])
		}

		to := toList[i]
		if cfg.RandomizeDst {
			var addr basics.Address
			crypto.RandBytes(addr[:])
			to = addr.String()
		}

		var noteField []byte
		const pingpongTag = "pingpong"
		const tagLen = uint32(len(pingpongTag))
		const randomBaseLen = uint32(8)
		const maxNoteFieldLen = uint32(1024)
		var noteLength = uint32(tagLen) + randomBaseLen
		// if random note flag set, then append a random number of additional bytes
		if cfg.RandomNote {
			noteLength = noteLength + rand.Uint32()%(maxNoteFieldLen-noteLength)
		}
		noteField = make([]byte, noteLength, noteLength)
		copy(noteField, pingpongTag)
		crypto.RandBytes(noteField[tagLen:])

		sentCount++
		_, sendErr := client.SendPaymentFromUnencryptedWallet(from, to, fee, amt, noteField[:])
		if sendErr != nil && !cfg.Quiet {
			fmt.Fprintf(os.Stderr, "error sending transaction: %v\n", err)
		} else {
			successCount++
		}
		if sendErr != nil {
			err = sendErr
			return
		}
		if cfg.DelayBetweenTxn > 0 {
			time.Sleep(cfg.DelayBetweenTxn)
		}
	}
	return
}

// generate a list of tlhc contract
func generateTLHC(fromList []string, toList []string) (programs [][]byte, addrs []string, err error) {
	pOut := make([][]byte, 0, len(fromList))
	pAddr := make([]string, 0, len(fromList))
	for i, from := range fromList {
		var program []byte
		var addr string
		source := tlhc(from, toList[i])
		program, addr, err = generateContract(source)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error generating tlhc program: %v\n", err)
			return
		}
		pAddr = append(pAddr, addr)
		pOut = append(pOut, program)
	}
	programs = pOut
	addrs = pAddr
	return
}

// send contract withdraw transactions
func contractWithdraw(contractList, toList []string, programs [][]byte, client libgoal.Client, cfg PpConfig) (sentCount, successCount uint64, err error) {
	amt := cfg.ContractWithdrawAmount
	fee := cfg.MaxFee
	for i, from := range contractList {

		if cfg.RandomizeFee {
			fee = rand.Uint64()%(cfg.MaxFee-cfg.MinFee) + cfg.MinFee
		}

		if !cfg.Quiet {
			fmt.Fprintf(os.Stdout, "contract withdraw %d : %v -> %s\n", amt, from, toList[i])
		}

		to := toList[i]

		var noteField []byte
		const pingpongTag = "pingpong-teal"
		const tagLen = uint32(len(pingpongTag))
		const randomBaseLen = uint32(8)
		const maxNoteFieldLen = uint32(1024)
		var noteLength = uint32(tagLen) + randomBaseLen
		// if random note flag set, then append a random number of additional bytes
		if cfg.RandomNote {
			noteLength = noteLength + rand.Uint32()%(maxNoteFieldLen-noteLength)
		}
		noteField = make([]byte, noteLength, noteLength)
		copy(noteField, pingpongTag)
		crypto.RandBytes(noteField[tagLen:])

		sentCount++

		// construct the transaction
		payment, constructErr := client.ConstructPayment(from, to, fee, amt, noteField, "", 0, 0)
		arg1, _ := base64.StdEncoding.DecodeString("xPUB+DJir1wsH7g2iEY1QwYqHqYH1vUJtzZKW4RxXsY=")
		programArgs := [][]byte{arg1}
		if constructErr != nil {
			err = constructErr
			if !cfg.Quiet {
				fmt.Fprintf(os.Stderr, "error construction txn: %v\n", err)
			}
			return
		}

		var stx transactions.SignedTxn
		if cfg.TLHC {
			stx = transactions.SignedTxn{
				Txn: payment,
				Lsig: transactions.LogicSig{
					Logic: programs[i],
					Args:  programArgs,
				}}
		} else {
			stx = transactions.SignedTxn{
				Txn: payment,
				Lsig: transactions.LogicSig{
					Logic: programs[i],
				}}
		}

		// send the transaction
		_, sendErr := client.BroadcastTransaction(stx)

		if sendErr != nil {
			if !cfg.Quiet {
			//programstr, _ := logic.Disassemble(programs[i])
			fmt.Fprintf(os.Stderr, "error sending transaction: %v\n", sendErr)
			//fmt.Fprintf(os.Stderr, "%s\n", programstr)
			}
			err = sendErr
			//return
		}

		successCount++

		if cfg.DelayBetweenTxn > 0 {
			time.Sleep(cfg.DelayBetweenTxn)
		}
	}
	return
}

// refresh contract accounts
func refreshContractAccount(accounts []string, client libgoal.Client, cfg PpConfig) error {

	// Fee of 0 will make cause the function to use the suggested one by network
	fee := cfg.MaxFee
	srcFunds, err := client.GetBalance(cfg.SrcAccount)
	if err != nil {
		return err
	}
	var contractMinAmount uint64
	if cfg.RefreshTime == 0 {
		contractMinAmount = 1000*3600*(cfg.ContractWithdrawAmount +cfg.MaxFee)
	} else {
		contractMinAmount = 1000*uint64(cfg.RefreshTime.Seconds())*(cfg.ContractWithdrawAmount +cfg.MaxFee)
	}

	const pingpongTag = "pingpong-tlhc-refund"
	const tagLen = uint32(len(pingpongTag))
	const randomBaseLen = uint32(8)
	var noteLength = uint32(tagLen) + randomBaseLen
	noteField := make([]byte, noteLength, noteLength)
	copy(noteField, pingpongTag)
	crypto.RandBytes(noteField[tagLen:])

	for _, addr := range accounts {
		balance, _ := client.GetBalance(addr)
		if balance < contractMinAmount {
			toSend := contractMinAmount - balance
			if srcFunds <= toSend {
				return fmt.Errorf("source account has insufficient funds %d - needs %d", srcFunds, toSend)
			}
			srcFunds -= toSend
			_, err := client.SendPaymentFromUnencryptedWallet(cfg.SrcAccount, addr, fee, toSend, noteField)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// generate teal programs that contains 10 signature verifications
func generateAirdrop(number int) (programs [][]byte, addrs []string, err error){
	var pBuf [][]byte
	var aBuf []string
	for i := 0; i < number; i++ {
		rBytes := make([]byte, 5, 5)
		crypto.RandBytes(rBytes)
		bStr := base32.StdEncoding.EncodeToString(rBytes)
		prefix := fmt.Sprintf("byte base32 %s\npop\n", bStr)
		p, a, genErr := generateContract(prefix+airdropTeal)
		if genErr != nil {
			fmt.Fprintf(os.Stderr,"error when generating dirty teal contract: %v\n", genErr)
			err = genErr
			return
		}
		pBuf = append(pBuf, p)
		aBuf = append(aBuf, a)
	}
	programs = pBuf
	addrs = aBuf
	return
}

// generate teal programs that contains 10 signature verifications
func generateDirtyTeal(number int) (programs [][]byte, addrs []string, err error){
	var pBuf [][]byte
	var aBuf []string
	for i := 0; i < number; i++ {
		rBytes := make([]byte, 5, 5)
		crypto.RandBytes(rBytes)
		bStr := base32.StdEncoding.EncodeToString(rBytes)
		prefix := fmt.Sprintf("byte base32 %s\npop\n", bStr)
		p, a, genErr := generateContract(prefix+dirtyTeal)
		if genErr != nil {
			fmt.Fprintf(os.Stderr,"error when generating dirty teal contract: %v\n", genErr)
			err = genErr
			return
		}
		pBuf = append(pBuf, p)
		aBuf = append(aBuf, a)
	}
	programs = pBuf
	addrs = aBuf
	return
}

// generate teal byte code and its hash from program string
func generateContract(pStr string) (program []byte, address string, err error) {
	var tp []byte
	tp, err = logic.AssembleString(pStr)
	if err != nil {
		fmt.Fprintf(os.Stderr,"error when assemble airdrop program: %v\n", err)
		fmt.Fprintf(os.Stderr," %s\n", pStr)
		return
	}
	program = tp
	ph := transactions.HashProgram(tp)
	pha := basics.Address(ph)
	address = pha.String()
	return
}
