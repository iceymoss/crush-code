package event

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"

	"github.com/denisbrodbeck/machineid"
)

// distinctId 是唯一标识符
var distinctId string

const (
	hashKey    = "charm"   // 哈希密钥
	fallbackId = "unknown" // 备用ID
)

// getDistinctId 获取唯一标识符
func getDistinctId() string {
	// 使用机器ID作为唯一标识符
	if id, err := machineid.ProtectedID(hashKey); err == nil {
		return id
	}
	// 使用MAC地址作为唯一标识符
	if macAddr, err := getMacAddr(); err == nil {
		return hashString(macAddr)
	}
	return fallbackId
}

// getMacAddr 获取MAC地址
func getMacAddr() (string, error) {
	// 获取网络接口
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	// 遍历网络接口
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 && len(iface.HardwareAddr) > 0 {
			// 获取网络地址
			if addrs, err := iface.Addrs(); err == nil && len(addrs) > 0 {
				// 返回MAC地址
				return iface.HardwareAddr.String(), nil
			}
			// 如果没有网络地址，则返回错误
			return "", fmt.Errorf("no active interface with mac address found")
		}
	}
	// 如果没有网络接口，则返回错误
	return "", fmt.Errorf("no active interface with mac address found")
}

// hashString 哈希字符串
func hashString(str string) string {
	// 使用HMAC-SHA256算法哈希字符串
	hash := hmac.New(sha256.New, []byte(str))
	// 写入哈希密钥
	hash.Write([]byte(hashKey))
	// 返回哈希值的十六进制编码
	return hex.EncodeToString(hash.Sum(nil))
}
