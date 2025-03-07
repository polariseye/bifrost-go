package client

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/JFJun/bifrost-go/base"
	"github.com/JFJun/bifrost-go/expand"
	"github.com/JFJun/bifrost-go/models"
	"github.com/JFJun/bifrost-go/utils"
	"github.com/JFJun/go-substrate-crypto/ss58"
	gsrc "github.com/stafiprotocol/go-substrate-rpc-client"
	gsClient "github.com/stafiprotocol/go-substrate-rpc-client/client"
	"github.com/stafiprotocol/go-substrate-rpc-client/rpc"
	"github.com/stafiprotocol/go-substrate-rpc-client/scale"
	"github.com/stafiprotocol/go-substrate-rpc-client/types"
	"golang.org/x/crypto/blake2b"
	"log"
	"math/big"
	"strconv"
	"strings"
)

type Client struct {
	C                  *gsrc.SubstrateAPI
	Meta               *types.Metadata
	prefix             []byte //币种的前缀
	ChainName          string //链名字
	SpecVersion        int
	TransactionVersion int
	genesisHash        string
	BasicType          *base.BasicTypes
	url                string
}

func New(url string, noPalletIndices bool) (*Client, error) {
	c := new(Client)
	c.url = url
	var err error
	//注册链的基本信息
	c.BasicType, err = base.InitBasicTypesByHexData()
	if err != nil {
		return nil, fmt.Errorf("init base type error: %v", err)
	}
	// 初始化rpc客户端
	c.C, err = gsrc.NewSubstrateAPI(url)
	if err != nil {
		return nil, err
	}
	//检查当前链运行的版本
	err = c.checkRuntimeVersion()
	if err != nil {
		return nil, err
	}
	/*
		设置prefix
	*/
	if len(c.prefix) == 0 {
		c.prefix, _ = c.BasicType.GetChainPrefix(c.ChainName)
	}
	//设置默认地址不需要0xff
	expand.SetSerDeOptions(noPalletIndices)
	return c, nil
}

func (c *Client) reConnectWs() (*gsrc.SubstrateAPI, error) {
	cl, err := gsClient.Connect(c.url)
	if err != nil {
		return nil, err
	}
	newRPC, err := rpc.NewRPC(cl)
	if err != nil {
		return nil, err
	}
	return &gsrc.SubstrateAPI{
		RPC:    newRPC,
		Client: cl,
	}, nil
}

func (c *Client) checkRuntimeVersion() error {
	v, err := c.C.RPC.State.GetRuntimeVersionLatest()
	if err != nil {
		if !strings.Contains(err.Error(), "tls: use of closed connection") {
			return fmt.Errorf("init runtime version error,err=%v", err)
		}
		//	重连处理，这是因为第三方包的问题，所以只能这样处理了了
		cl, err := c.reConnectWs()
		if err != nil {
			return fmt.Errorf("reconnect error: %v", err)
		}
		c.C = cl
		v, err = c.C.RPC.State.GetRuntimeVersionLatest()
		if err != nil {
			return fmt.Errorf("init runtime version error,aleady reconnect,err: %v", err)
		}
	}
	c.TransactionVersion = int(v.TransactionVersion)
	c.ChainName = v.SpecName
	specVersion := int(v.SpecVersion)
	//检查metadata数据是否有升级
	if specVersion != c.SpecVersion {
		c.Meta, err = c.C.RPC.State.GetMetadataLatest()
		if err != nil {
			return fmt.Errorf("init metadata error: %v", err)
		}
		c.SpecVersion = specVersion
	}
	return nil
}

/*
获取创世区块hash
*/
func (c *Client) GetGenesisHash() string {
	if c.genesisHash != "" {
		return c.genesisHash
	}
	hash, err := c.C.RPC.Chain.GetBlockHash(0)
	if err != nil {
		return ""
	}
	c.genesisHash = hash.Hex()
	return hash.Hex()
}

/*
自定义设置prefix，如果启动时加载的prefix是错误的，则需要手动配置prefix
*/
func (c *Client) SetPrefix(prefix []byte) {
	c.prefix = prefix
}

/*
根据height解析block，返回block是否包含交易
*/
func (c *Client) GetBlockByNumber(height int64) (*models.BlockResponse, error) {
	hash, err := c.C.RPC.Chain.GetBlockHash(uint64(height))
	if err != nil {
		return nil, fmt.Errorf("get block hash error:%v,height:%d", err, height)
	}
	blockHash := hash.Hex()

	return c.GetBlockByHash(blockHash)
}

func (c *Client) GetBlockHashByNumber(height int64) (*types.Hash, error) {
	hash, err := c.C.RPC.Chain.GetBlockHash(uint64(height))
	if err != nil {
		return nil, fmt.Errorf("get block hash error:%v,height:%d", err, height)
	}
	return &hash, nil
}

/*
根据blockHash解析block，返回block是否包含交易
*/
func (c *Client) GetBlockByHash(blockHash string) (*models.BlockResponse, error) {
	var (
		block *models.SignedBlock
		err   error
	)
	err = c.checkRuntimeVersion()
	if err != nil {
		return nil, err
	}
	err = c.C.Client.Call(&block, "chain_getBlock", blockHash)
	if err != nil {
		return nil, fmt.Errorf("get block error: %v", err)
	}
	blockResp := new(models.BlockResponse)
	number, _ := strconv.ParseInt(utils.RemoveHex0x(block.Block.Header.Number), 16, 64)
	blockResp.Height = number
	blockResp.ParentHash = block.Block.Header.ParentHash
	blockResp.BlockHash = blockHash
	if len(block.Block.Extrinsics) > 0 {
		err = c.parseExtrinsicByDecode(block.Block.Extrinsics, blockResp)
		if err != nil {
			return nil, err
		}
		err = c.parseExtrinsicByStorage(blockHash, blockResp)
		if err != nil {
			return nil, err
		}
	}
	return blockResp, nil
}

type parseBlockExtrinsicParams struct {
	from, to, sig, era, txid string
	nonce                    int64
	extrinsicIdx, length     int
	amount                   string
	Fee                      string
}

/*
解析外部交易extrinsic
*/
func (c *Client) parseExtrinsicByDecode(extrinsics []string, blockResp *models.BlockResponse) error {
	var (
		params    []parseBlockExtrinsicParams
		timestamp int64
		//idx int
	)
	defer func() {
		if err := recover(); err != nil {
			blockResp.Timestamp = timestamp
			blockResp.Extrinsic = []*models.ExtrinsicResponse{}
			log.Printf("parse %d block extrinsic error,Err=[%v]", blockResp.Height, err)
		}
	}()

	for i, extrinsic := range extrinsics {
		extrinsic = utils.Remove0X(extrinsic)
		data, err := hex.DecodeString(extrinsic)
		if err != nil {
			return fmt.Errorf("hex.decode extrinsic error: %v", err)
		}
		decoder := scale.NewDecoder(bytes.NewReader(data))
		ed, err := expand.NewExtrinsicDecoder(c.Meta)
		if err != nil {
			return fmt.Errorf("new extrinsic decode error: %v", err)
		}
		err = ed.ProcessExtrinsicDecoder(*decoder)
		if err != nil {
			return fmt.Errorf("decode extrinsic error: %v", err)
		}
		var resp models.ExtrinsicDecodeResponse
		d, _ := json.Marshal(ed.Value)
		if len(d) == 0 {
			return errors.New("unknown extrinsic decode response")
		}
		err = json.Unmarshal(d, &resp)
		if err != nil {
			return fmt.Errorf("json unmarshal extrinsic decode error: %v", err)
		}
		switch resp.CallModule {
		case "Timestamp":
			for _, param := range resp.Params {
				if param.Name == "now" {
					timestamp = int64(param.Value.(float64))
				}
			}
		case "Balances":
			if resp.CallModuleFunction == "transfer" || resp.CallModuleFunction == "transfer_keep_alive" {
				blockData := parseBlockExtrinsicParams{}
				blockData.from, _ = ss58.EncodeByPubHex(resp.AccountId, c.prefix)
				blockData.era = resp.Era
				blockData.sig = resp.Signature
				blockData.nonce = resp.Nonce
				blockData.extrinsicIdx = i
				blockData.Fee, err = c.GetPartialFee(extrinsic, blockResp.ParentHash)
				blockData.txid = c.createTxHash(extrinsic)
				blockData.length = resp.Length
				for _, param := range resp.Params {
					if param.Name == "dest" {
						blockData.to, _ = ss58.EncodeByPubHex(param.Value.(string), c.prefix)
					}
					if param.Name == "value" {
						blockData.amount = param.Value.(string)
					}
				}
				params = append(params, blockData)
			}

		case "Utility":
			if resp.CallModuleFunction == "batch" {
				for _, param := range resp.Params {
					if param.Name == "calls" {
						switch param.Value.(type) {
						case []interface{}:

							d, _ := json.Marshal(param.Value)
							var values []models.UtilityParamsValue
							err = json.Unmarshal(d, &values)
							if err != nil {
								continue
							}

							for _, value := range values {
								if value.CallModule == "Balances" {
									if value.CallFunction == "transfer" || value.CallFunction == "transfer_keep_alive" {
										if len(value.CallArgs) > 0 {
											for _, arg := range value.CallArgs {
												if arg.Name == "dest" {
													blockData := parseBlockExtrinsicParams{}
													blockData.from, _ = ss58.EncodeByPubHex(resp.AccountId, c.prefix)
													blockData.era = resp.Era
													blockData.sig = resp.Signature
													blockData.nonce = resp.Nonce
													blockData.extrinsicIdx = i
													blockData.Fee, _ = c.GetPartialFee(extrinsic, blockResp.ParentHash)
													blockData.txid = c.createTxHash(extrinsic)
													blockData.to, _ = ss58.EncodeByPubHex(arg.ValueRaw, c.prefix)
													params = append(params, blockData)
												}
											}
										}
									}
								}
							}
						default:
							continue
						}
					}
				}
			}
		default:
			//todo  add another call_module 币种不同可能使用的call_module不一样
			continue
		}
	}
	blockResp.Timestamp = timestamp
	//解析params
	if len(params) == 0 {
		blockResp.Extrinsic = []*models.ExtrinsicResponse{}
		return nil
	}

	blockResp.Extrinsic = make([]*models.ExtrinsicResponse, len(params))
	for idx, param := range params {
		e := new(models.ExtrinsicResponse)
		e.Signature = param.sig
		e.FromAddress = param.from
		e.ToAddress = param.to
		e.Nonce = param.nonce
		e.Era = param.era
		e.Fee = param.Fee
		e.ExtrinsicIndex = param.extrinsicIdx
		e.Amount = param.amount
		//e.Txid = txid
		e.Txid = param.txid
		e.ExtrinsicLength = param.length
		blockResp.Extrinsic[idx] = e

	}
	//utils.CheckStructData(blockResp)
	return nil
}

/*
解析当前区块的System.event
*/
func (c *Client) parseExtrinsicByStorage(blockHash string, blockResp *models.BlockResponse) error {
	var (
		storage types.StorageKey
		err     error
	)
	defer func() {
		if err1 := recover(); err1 != nil {
			err = fmt.Errorf("panic decode event: %v", err1)
		}
	}()
	if len(blockResp.Extrinsic) <= 0 {
		//不包含交易就不处理了
		return nil
	}
	// 1. 先创建System.event的storageKey
	storage, err = types.CreateStorageKey(c.Meta, "System", "Events", nil, nil)
	if err != nil {
		return fmt.Errorf("create storage key error: %v", err)
	}
	key := storage.Hex()
	var result interface{}
	/*
		根据storageKey以及blockHash获取当前区块的event信息
	*/
	err = c.C.Client.Call(&result, "state_getStorageAt", key, blockHash)
	if err != nil {
		return fmt.Errorf("get storage data error: %v", err)
	}

	//解析event信息
	ier, err := expand.DecodeEventRecords(c.Meta, result.(string), c.ChainName)

	if err != nil {
		return fmt.Errorf("decode event data error: %v", err)
	}
	//d,_:=json.Marshal(ier)
	//fmt.Println(string(d))
	var res []models.EventResult
	failedMap := make(map[int]bool)
	if len(ier.GetBalancesTransfer()) > 0 {
		//有失败的交易
		for _, failed := range ier.GetSystemExtrinsicFailed() {
			if failed.Phase.IsApplyExtrinsic {
				extrinsicIdx := failed.Phase.AsApplyExtrinsic
				//记录到失败的map中
				failedMap[int(extrinsicIdx)] = true
			}
		}

		for _, ebt := range ier.GetBalancesTransfer() {

			if !ebt.Phase.IsApplyExtrinsic {
				continue
			}
			extrinsicIdx := int(ebt.Phase.AsApplyExtrinsic)
			var r models.EventResult
			r.ExtrinsicIdx = extrinsicIdx
			fromHex := hex.EncodeToString(ebt.From[:])
			r.From, err = ss58.EncodeByPubHex(fromHex, c.prefix)
			if err != nil {
				r.From = ""
				continue
			}
			toHex := hex.EncodeToString(ebt.To[:])

			r.To, err = ss58.EncodeByPubHex(toHex, c.prefix)
			if err != nil {
				r.To = ""
				continue
			}
			r.Amount = ebt.Value.String()
			//r.Weight = c.getWeight(&events, r.ExtrinsicIdx)
			res = append(res, r)
		}
	}
	for _, e := range blockResp.Extrinsic {
		e.Status = "fail"
		e.Type = "transfer"
		if len(res) > 0 {
			for _, r := range res {
				if e.ExtrinsicIndex == r.ExtrinsicIdx {
					if e.ToAddress == r.To {
						if failedMap[e.ExtrinsicIndex] {
							e.Status = "fail"
						} else {
							e.Status = "success"
						}
						e.Type = "transfer"
						e.Amount = r.Amount
						e.ToAddress = r.To
						//计算手续费
						//e.Fee = c.calcFee(&events, e.ExtrinsicIndex)
					}
				}
			}
		}
	}

	return nil
}

/*
根据外部交易extrinsic创建txid
*/
func (c *Client) createTxHash(extrinsic string) string {
	data, _ := hex.DecodeString(utils.RemoveHex0x(extrinsic))
	d := blake2b.Sum256(data)
	return "0x" + hex.EncodeToString(d[:])
}

/*
根据地址获取地址的账户信息，包括nonce以及余额等
*/
func (c *Client) GetAccountInfo(address string) (*types.AccountInfo, error) {
	var (
		storage types.StorageKey
		err     error
		pub     []byte
	)
	defer func() {
		if err1 := recover(); err1 != nil {
			err = fmt.Errorf("panic decode event: %v", err1)
		}
	}()
	err = c.checkRuntimeVersion()
	if err != nil {
		return nil, err
	}
	pub, err = ss58.DecodeToPub(address)
	if err != nil {
		return nil, fmt.Errorf("ss58 decode address error: %v", err)
	}
	storage, err = types.CreateStorageKey(c.Meta, "System", "Account", pub, nil)
	if err != nil {
		return nil, fmt.Errorf("create System.Account storage error: %v", err)
	}
	var accountInfo types.AccountInfo
	var ok bool
	switch strings.ToLower(c.ChainName) {
	// todo 目前这里先做硬编码先，后续在进行修改
	case "polkadot", "kusama":
		var accountInfoProviders expand.AccountInfoWithProviders
		ok, err = c.C.RPC.State.GetStorageLatest(storage, &accountInfoProviders)
		if err != nil || !ok {
			return nil, fmt.Errorf("get account info error: %v", err)
		}
		accountInfo.Nonce = accountInfoProviders.Nonce
		accountInfo.Refcount = accountInfoProviders.Consumers
		accountInfo.Data.Free = accountInfoProviders.Data.Free
		accountInfo.Data.FreeFrozen = accountInfoProviders.Data.FreeFrozen
		accountInfo.Data.MiscFrozen = accountInfoProviders.Data.MiscFrozen
		accountInfo.Data.Reserved = accountInfoProviders.Data.Reserved
	default:
		ok, err = c.C.RPC.State.GetStorageLatest(storage, &accountInfo)
		if err != nil || !ok {
			return nil, fmt.Errorf("get account info error: %v", err)
		}
	}

	return &accountInfo, nil
}

/*
获取外部交易extrinsic的手续费
*/
func (c *Client) GetPartialFee(extrinsic, parentHash string) (string, error) {
	if !strings.HasPrefix(extrinsic, "0x") {
		extrinsic = "0x" + extrinsic
	}
	var result map[string]interface{}
	err := c.C.Client.Call(&result, "payment_queryInfo", extrinsic, parentHash)
	if err != nil {
		return "", fmt.Errorf("get payment info error: %v", err)
	}
	if result["partialFee"] == nil {
		return "", errors.New("result partialFee is nil ptr")
	}
	fee, ok := result["partialFee"].(string)
	if !ok {
		return "", fmt.Errorf("partialFee is not string type: %v", result["partialFee"])
	}
	return fee, nil
}

func (c *Client) GetPartialFeeDetail(extrinsic, parentHash string) (*expand.FeeDetail, error) {
	if !strings.HasPrefix(extrinsic, "0x") {
		extrinsic = "0x" + extrinsic
	}
	var result map[string]interface{}
	err := c.C.Client.Call(&result, "payment_queryFeeDetails", extrinsic, parentHash)
	if err != nil {
		return nil, fmt.Errorf("get payment info error: %v", err)
	}
	result = result["inclusionFee"].(map[string]interface{})
	var resultObj = &expand.FeeDetail{}
	decodeFunc := func(val string) types.U128 {
		if strings.HasPrefix(val, "0x") {
			val = val[2:]
		}

		bigVal := big.NewInt(0)
		bigVal.SetString(val, 16)
		return types.NewU128(*bigVal)
	}

	resultObj.LenFee = decodeFunc(result["lenFee"].(string))
	resultObj.BaseFee = decodeFunc(result["baseFee"].(string))
	resultObj.AdjustedWeightFee = decodeFunc(result["adjustedWeightFee"].(string))
	return resultObj, nil
}
