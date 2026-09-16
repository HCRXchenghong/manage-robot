package main

// gateway_bootstrap.go：独立的 Gateway mTLS 激活端口。
// 浏览器运营入口不承载设备证书；本端口只接受 RequireAndVerifyClientCert
// 校验后的设备连接，避免被反向代理 header 或普通 Cookie 冒充车端身份。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

type gatewayEnrollRequest struct {
	VehicleID       string `json:"vehicle_id"`
	GatewayID       string `json:"gateway_id"`
	EnrollmentToken string `json:"enrollment_token"`
}

func startGatewayBootstrap(addr, certFile, keyFile, clientCA string, registry *GatewayRegistry, st *State) error {
	if addr == "" {
		return nil
	}
	if certFile == "" || keyFile == "" || clientCA == "" {
		return fmt.Errorf("启用 Gateway bootstrap 时必须同时提供服务端证书、私钥与客户端 CA")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("读取 bootstrap 服务端证书: %w", err)
	}
	caPEM, err := os.ReadFile(clientCA)
	if err != nil {
		return fmt.Errorf("读取 bootstrap 客户端 CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("解析 bootstrap 客户端 CA 失败")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gateway/enroll", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "需要且只接受一张 Gateway mTLS 客户端证书"})
			return
		}
		defer r.Body.Close()
		r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
		var req gatewayEnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式无效"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		identity, err := registry.Activate(ctx, req.VehicleID, req.GatewayID, req.EnrollmentToken, r.TLS.PeerCertificates[0])
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Gateway 激活被拒绝"})
			log.Printf("[gateway-bootstrap] 拒绝 vehicle=%q gateway=%q：%v", req.VehicleID, req.GatewayID, err)
			return
		}
		if st != nil {
			st.pushEvent("warn", identity.VehicleID, "Gateway 已完成 mTLS 身份激活，等待受绑定 MQTT 连接", "sys")
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"ok": true, "vehicle_id": identity.VehicleID, "gateway_id": identity.GatewayID,
			"certificate_not_after": identity.CertificateUntil.UTC().Format(time.RFC3339),
		})
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    pool,
			Certificates: []tls.Certificate{cert},
		},
	}
	go func() {
		log.Printf("[gateway-bootstrap] mTLS 激活端口监听 %s", addr)
		if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Printf("[gateway-bootstrap] 已停止：%v", err)
		}
	}()
	return nil
}
