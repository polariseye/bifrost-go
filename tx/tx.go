package tx

import (
	"encoding/hex"
	"fmt"
	"github.com/JFJun/bifrost-go/utils"
	"github.com/JFJun/bifrost-go/expand"
	"github.com/JFJun/go-substrate-crypto/crypto"
	"github.com/stafiprotocol/go-substrate-rpc-client/types"
	"golang.org/x/crypto/blake2b"
	"strings"
)

/*
======================================= expand transaction===================================
由于许多的链的交易格式稍有不同，所以决定交易结构进行更细 的拆分
*/

type SubstrateTransaction struct {
	SenderPubkey       string `json:"sender_pubkey"` // from address public key ,0x开头
	Nonce              uint64 `json:"nonce"`         //nonce值
	BlockHash          string `json:"block_hash"`    //最新区块hash
	GenesisHash        string `json:"genesis_hash"`  //
	SpecVersion        uint32 `json:"spec_version"`
	TransactionVersion uint32 `json:"transaction_version"`
	Tip                uint64 `json:"tip"`          //小费
	BlockNumber        uint64 `json:"block_Number"` //最新区块高度
	EraPeriod          uint64 `json:"era_period"`   // 存活最大区块
	call               types.Call
}

func NewSubstrateTransaction(from string, nonce uint64) *SubstrateTransaction {
	st := new(SubstrateTransaction)
	st.SenderPubkey = utils.AddressToPublicKey(from)
	st.Nonce = nonce
	return st
}

/*
设置交易的必要参数genesisHash和blockHash
*/
func (tx *SubstrateTransaction) SetGenesisHashAndBlockHash(genesisHash, blockHash string) *SubstrateTransaction {
	tx.GenesisHash = utils.Remove0X(genesisHash)
	tx.BlockHash = utils.Remove0X(blockHash)
	return tx
}

/*
设置链的版本以及交易版本
*/
func (tx *SubstrateTransaction) SetSpecAndTxVersion(specVersion, transactionVersion uint32) *SubstrateTransaction {
	tx.SpecVersion = specVersion
	tx.TransactionVersion = transactionVersion
	return tx
}

/*
设置链的版本以及交易版本
*/
func (tx *SubstrateTransaction) SetSpecVersionAndCallId(specVersion, transactionVersion uint32) *SubstrateTransaction {
	tx.SpecVersion = specVersion
	tx.TransactionVersion = transactionVersion
	return tx
}

/*
给矿工增加手续费，可以加快打包速度
*/
func (tx *SubstrateTransaction) SetTip(tip uint64) *SubstrateTransaction {
	tx.Tip = tip
	return tx
}

/*
设置如果交易一直处于pending中，最多存活多少个块
*/
func (tx *SubstrateTransaction) SetEra(blockNumber, eraPeriod uint64) *SubstrateTransaction {
	tx.BlockNumber = blockNumber
	tx.EraPeriod = eraPeriod
	return tx
}
func (tx *SubstrateTransaction) SetCall(call types.Call) *SubstrateTransaction {
	tx.call = call
	return tx
}


func (tx *SubstrateTransaction) ReturnSign() (*expand.Extrinsic,types.SignatureOptions,[]byte,error){
	ext := expand.NewExtrinsic(tx.call)
	o := types.SignatureOptions{
		BlockHash:          types.NewHash(types.MustHexDecodeString(tx.BlockHash)),
		GenesisHash:        types.NewHash(types.MustHexDecodeString(tx.GenesisHash)),
		Nonce:              types.NewUCompactFromUInt(tx.Nonce),
		SpecVersion:        types.NewU32(tx.SpecVersion),
		Tip:                types.NewUCompactFromUInt(tx.Tip),
		TransactionVersion: types.NewU32(tx.TransactionVersion),
	}
	era := tx.getEra()
	if era != nil {
		o.Era = *era
	}
	if ext.Type() != types.ExtrinsicVersion4 {
		return &expand.Extrinsic{}, types.SignatureOptions{},nil,fmt.Errorf("unsupported extrinsic version: %v (isSigned: %v, type: %v)", ext.Version, ext.IsSigned(), ext.Type())
	}
	mb, err := types.EncodeToBytes(ext.Method)
	if err != nil {
		return &expand.Extrinsic{}, types.SignatureOptions{},nil,err
	}
	eras := o.Era
	if !o.Era.IsMortalEra {
		eras = types.ExtrinsicEra{IsImmortalEra: true}
	}
	payload := types.ExtrinsicPayloadV4{
		ExtrinsicPayloadV3: types.ExtrinsicPayloadV3{
			Method:      mb,
			Era:         eras,
			Nonce:       o.Nonce,
			Tip:         o.Tip,
			SpecVersion: o.SpecVersion,
			GenesisHash: o.GenesisHash,
			BlockHash:   o.BlockHash,
		},
		TransactionVersion: o.TransactionVersion,
	}
	data, err := types.EncodeToBytes(payload)
	if err != nil {
		return &expand.Extrinsic{}, types.SignatureOptions{},nil,fmt.Errorf("encode payload error: %v", err)
	}
	if len(data) > 256 {
		h := blake2b.Sum256(data)
		data = h[:]
	}
	return &ext,o,data,nil
}

func (tx *SubstrateTransaction) SignTransaction(privateKey string, signType int) (string, error) {

	ext := expand.NewExtrinsic(tx.call)
	o := types.SignatureOptions{
		BlockHash:          types.NewHash(types.MustHexDecodeString(tx.BlockHash)),
		GenesisHash:        types.NewHash(types.MustHexDecodeString(tx.GenesisHash)),
		Nonce:              types.NewUCompactFromUInt(tx.Nonce),
		SpecVersion:        types.NewU32(tx.SpecVersion),
		Tip:                types.NewUCompactFromUInt(tx.Tip),
		TransactionVersion: types.NewU32(tx.TransactionVersion),
	}
	era := tx.getEra()
	if era != nil {
		o.Era = *era
	}
	e := &ext
	//签名
	err := tx.signTx(e, o, privateKey, signType)
	if err != nil {
		return "", fmt.Errorf("sign error: %v", err)
	}
	return types.EncodeToHexString(e)
}

func (tx *SubstrateTransaction) signTx(e *expand.Extrinsic, o types.SignatureOptions, privateKey string, signType int) error {
	if e.Type() != types.ExtrinsicVersion4 {
		return fmt.Errorf("unsupported extrinsic version: %v (isSigned: %v, type: %v)", e.Version, e.IsSigned(), e.Type())
	}
	mb, err := types.EncodeToBytes(e.Method)
	if err != nil {
		return err
	}
	era := o.Era
	if !o.Era.IsMortalEra {
		era = types.ExtrinsicEra{IsImmortalEra: true}
	}
	payload := types.ExtrinsicPayloadV4{
		ExtrinsicPayloadV3: types.ExtrinsicPayloadV3{
			Method:      mb,
			Era:         era,
			Nonce:       o.Nonce,
			Tip:         o.Tip,
			SpecVersion: o.SpecVersion,
			GenesisHash: o.GenesisHash,
			BlockHash:   o.BlockHash,
		},
		TransactionVersion: o.TransactionVersion,
	}
	// sign
	data, err := types.EncodeToBytes(payload)
	if err != nil {
		return fmt.Errorf("encode payload error: %v", err)
	}
	// if data is longer than 256 bytes, hash it first
	if len(data) > 256 {
		h := blake2b.Sum256(data)
		data = h[:]
	}
	privateKey = strings.TrimPrefix(privateKey, "0x")
	priv, err := hex.DecodeString(privateKey)
	if err != nil {
		return fmt.Errorf("hex decode private key error: %v", err)
	}

	defer utils.ZeroBytes(priv)
	sig, err := crypto.Sign(priv, data, signType)
	if err != nil {
		return fmt.Errorf("sign error: %v", err)
	}

	var ma expand.MultiAddress
	ma.SetTypes(0)
	ma.AccountId = types.NewAccountID(types.MustHexDecodeString(
		tx.SenderPubkey))

	var ss types.MultiSignature
	if signType == crypto.Ed25519Type {
		ss = types.MultiSignature{IsEd25519: true, AsEd25519: types.NewSignature(sig)}
	} else if signType == crypto.Sr25519Type {
		ss = types.MultiSignature{IsSr25519: true, AsSr25519: types.NewSignature(sig)}
	} else if signType == crypto.EcdsaType {
		ss = types.MultiSignature{IsEcdsa: true, AsEcdsa: types.NewBytes(sig)}
	} else {
		return fmt.Errorf("unsupport sign type : %d", signType)
	}
	extSig := expand.ExtrinsicSignatureV4{
		Signer:    ma,
		Signature: ss,
		Era:       era,
		Nonce:     o.Nonce,
		Tip:       o.Tip,
	}
	e.Signature = extSig
	e.Version |= types.ExtrinsicBitSigned
	return nil
}
func (tx *SubstrateTransaction) getEra() *types.ExtrinsicEra {
	if tx.BlockNumber == 0 || tx.EraPeriod == 0 {
		return nil
	}
	phase := tx.BlockNumber % tx.EraPeriod
	index := uint64(6)
	trailingZero := index - 1

	var encoded uint64
	if trailingZero > 1 {
		encoded = trailingZero
	} else {
		encoded = 1
	}

	if trailingZero < 15 {
		encoded = trailingZero
	} else {
		encoded = 15
	}
	encoded += phase / 1 << 4
	first := byte(encoded >> 8)
	second := byte(encoded & 0xff)
	era := new(types.ExtrinsicEra)
	era.IsMortalEra = true
	era.AsMortalEra.First = first
	era.AsMortalEra.Second = second
	return era
}
