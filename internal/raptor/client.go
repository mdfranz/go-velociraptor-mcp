package raptor

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	vql_proto "www.velocidex.com/golang/velociraptor/actions/proto"
	api_proto "www.velocidex.com/golang/velociraptor/api/proto"
)

type Client struct {
	conn *grpc.ClientConn
	stub api_proto.APIClient
	cfg  *Config
}

func NewClient(cfg *Config) (*Client, error) {
	cert, err := tls.X509KeyPair([]byte(cfg.ClientCert), []byte(cfg.ClientPrivateKey))
	if err != nil {
		return nil, fmt.Errorf("parse client keypair: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(cfg.CACertificate)) {
		return nil, fmt.Errorf("parse ca_certificate: no valid PEM blocks found")
	}

	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   cfg.PinnedServerName,
	})

	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	if cfg.MaxGRPCRecvSize > 0 {
		opts = append(opts, grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(cfg.MaxGRPCRecvSize),
		))
	}

	conn, err := grpc.NewClient(cfg.APIConnectionString, opts...)
	if err != nil {
		return nil, fmt.Errorf("grpc connect %s: %w", cfg.APIConnectionString, err)
	}

	return &Client{
		conn: conn,
		stub: api_proto.NewAPIClient(conn),
		cfg:  cfg,
	}, nil
}

func (c *Client) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

func (c *Client) OrgID(override string) string {
	if override != "" {
		return override
	}
	return c.cfg.OrgID
}

func (c *Client) Health(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.cfg != nil && c.cfg.DefaultTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.DefaultTimeout)
		defer cancel()
	}

	response, err := c.stub.Check(ctx, &api_proto.HealthCheckRequest{})
	if err != nil {
		return "", err
	}
	return response.Status.String(), nil
}

// StreamVQL executes a VQL query and calls fn for each decoded batch of rows
// as they arrive from the gRPC stream. This avoids buffering the entire result
// set in memory and is used by the export tool to write directly to disk.
func (c *Client) StreamVQL(ctx context.Context, vql string, orgID string, fn func([]map[string]any) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.cfg != nil && c.cfg.DefaultTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.DefaultTimeout)
		defer cancel()
	}

	args := &vql_proto.VQLCollectorArgs{
		Query: []*vql_proto.VQLRequest{{VQL: vql}},
	}
	if orgID != "" {
		args.OrgId = orgID
	}

	stream, err := c.stub.Query(ctx, args)
	if err != nil {
		return err
	}

	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		chunk, err := decodeResponse(resp)
		if err != nil {
			return err
		}
		if len(chunk) > 0 {
			if err := fn(chunk); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) RunVQL(ctx context.Context, vql string, orgID string) ([]map[string]any, error) {
	var rows []map[string]any
	err := c.StreamVQL(ctx, vql, orgID, func(chunk []map[string]any) error {
		rows = append(rows, chunk...)
		return nil
	})
	return rows, err
}

func decodeResponse(resp *vql_proto.VQLResponse) ([]map[string]any, error) {
	if resp.UncompressedSize > 0 && len(resp.CompressedJsonResponse) > 0 {
		data, err := decompressZlib(resp.CompressedJsonResponse)
		if err != nil {
			return nil, err
		}
		return parseJSONL(data)
	}
	if resp.JSONLResponse != "" {
		return parseJSONL([]byte(resp.JSONLResponse))
	}
	if resp.Response != "" {
		var rows []map[string]any
		if err := json.Unmarshal([]byte(resp.Response), &rows); err != nil {
			return nil, err
		}
		return rows, nil
	}
	return nil, nil
}

func decompressZlib(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("zlib reader: %w", err)
	}
	defer r.Close()
	return io.ReadAll(r)
}

func parseJSONL(data []byte) ([]map[string]any, error) {
	var rows []map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var row map[string]any
		if err := dec.Decode(&row); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("jsonl decode: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}
