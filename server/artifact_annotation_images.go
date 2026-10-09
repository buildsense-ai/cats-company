package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

const artifactScreenshotMaxImageBytes = 2 << 20
const artifactScreenshotMaxDimension = 2048

// Local receipt only for authenticated image uploads. No bearer proof in app
// payload, no upload DB migration and no change to existing upload response.
// Stored outside served upload directories, mode0600, so an untrusted URL cannot
// invent ownership and receipts survive this handler/server being recreated.
type artifactImageUploadReceipt struct {
	ActorUID int64  `json:"actor_uid"`
	FileKey  string `json:"file_key"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

func (h *UploadHandler) artifactReceiptDir() string {
	return filepath.Join(h.baseDir, ".artifact-image-receipts")
}
func (h *UploadHandler) rememberArtifactImageUpload(uid int64, key string) error {
	if uid <= 0 {
		return nil
	} // Legacy/mobile anonymous upload remains ordinary, never owned screenshot proof.
	file, err := os.Open(filepath.Join(h.baseDir, "images", key))
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	// Larger ordinary uploads stay compatible, but cannot become screenshot
	// proof. Avoid a second read of a potentially 300MiB ordinary image.
	if !info.Mode().IsRegular() || info.Size() > artifactScreenshotMaxImageBytes {
		return nil
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return err
	}
	receipt := artifactImageUploadReceipt{ActorUID: uid, FileKey: key, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}
	if err := os.MkdirAll(h.artifactReceiptDir(), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(h.artifactReceiptDir(), ".receipt-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), filepath.Join(h.artifactReceiptDir(), key+".json"))
}

func (h *ArtifactOpenBindingHandler) SetUploadHandler(uploads *UploadHandler) { h.uploads = uploads }

func (h *UploadHandler) ownedArtifactScreenshot(uid int64, payload map[string]interface{}) (types.ContentBlock, error) {
	key, _ := payload["file_key"].(string)
	if !uploadFileNamePattern.MatchString(key) || !oneOf(strings.ToLower(filepath.Ext(key)), ".jpg", ".jpeg") {
		return types.ContentBlock{}, errors.New("screenshot file_key must name a JPEG image upload")
	}
	rawURL, _ := payload["url"].(string)
	canonicalURL := "/uploads/images/" + key
	// No request-host-derived origin or arbitrary URL fetch. Only own storage.
	if rawURL != canonicalURL && rawURL != strings.TrimRight(h.baseURL, "/")+"/images/"+key {
		return types.ContentBlock{}, errors.New("screenshot URL is not the canonical image upload")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.ForceQuery {
		return types.ContentBlock{}, errors.New("invalid screenshot URL")
	}
	role, _ := payload["screenshot_role"].(string)
	mime, _ := payload["mime_type"].(string)
	if !oneOf(role, "full", "crop") || mime != "image/jpeg" {
		return types.ContentBlock{}, errors.New("screenshot requires full/crop role and image/jpeg")
	}
	claimedSize, sizeOK := imageEditInteger(payload["size"])
	claimedWidth, widthOK := imageEditInteger(payload["width"])
	claimedHeight, heightOK := imageEditInteger(payload["height"])
	if !sizeOK || claimedSize <= 0 || claimedSize > artifactScreenshotMaxImageBytes || !widthOK || !heightOK || claimedWidth <= 0 || claimedHeight <= 0 || claimedWidth > artifactScreenshotMaxDimension || claimedHeight > artifactScreenshotMaxDimension {
		return types.ContentBlock{}, errors.New("invalid screenshot budget or dimensions")
	}
	receiptFile, err := os.Open(filepath.Join(h.artifactReceiptDir(), key+".json"))
	if err != nil {
		return types.ContentBlock{}, errors.New("screenshot has no owned upload receipt")
	}
	receiptData, err := io.ReadAll(io.LimitReader(receiptFile, 4097))
	_ = receiptFile.Close()
	if err != nil || len(receiptData) > 4096 {
		return types.ContentBlock{}, errors.New("invalid screenshot upload receipt")
	}
	var receipt artifactImageUploadReceipt
	if json.Unmarshal(receiptData, &receipt) != nil || receipt.ActorUID != uid || receipt.FileKey != key || receipt.Size != int64(claimedSize) {
		return types.ContentBlock{}, errors.New("screenshot upload does not belong to actor or size mismatches")
	}
	path := filepath.Join(h.baseDir, "images", key)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != receipt.Size {
		return types.ContentBlock{}, errors.New("screenshot file unavailable or changed")
	}
	root, err := filepath.EvalSymlinks(filepath.Join(h.baseDir, "images"))
	if err != nil {
		return types.ContentBlock{}, err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return types.ContentBlock{}, err
	}
	relative, err := filepath.Rel(root, real)
	if err != nil || relative != key {
		return types.ContentBlock{}, errors.New("screenshot escapes image storage")
	}
	file, err := os.Open(real)
	if err != nil {
		return types.ContentBlock{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, artifactScreenshotMaxImageBytes+1))
	if err != nil || len(data) != claimedSize {
		return types.ContentBlock{}, errors.New("screenshot bytes exceed budget or changed")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != receipt.SHA256 {
		return types.ContentBlock{}, errors.New("screenshot bytes changed after upload")
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != claimedWidth || config.Height != claimedHeight {
		return types.ContentBlock{}, errors.New("screenshot JPEG dimensions mismatch")
	}
	if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
		return types.ContentBlock{}, errors.New("screenshot is not a complete JPEG")
	}
	return types.ContentBlock{Type: "image", Payload: map[string]interface{}{
		"url": canonicalURL, "file_key": key, "name": "gateway-" + role + ".jpg", "mime_type": "image/jpeg", "size": claimedSize, "width": config.Width, "height": config.Height, "screenshot_role": role,
	}}, nil
}

// Canonicalize only bound annotation attachments; ordinary upload/send APIs keep
// their historical behavior. Text-only remains explicit by omitting images.
func (h *ArtifactOpenBindingHandler) validatedScreenshotBlocks(uid int64, blocks []types.ContentBlock) ([]types.ContentBlock, error) {
	imageCount := 0
	for _, block := range blocks {
		if block.Type == "image" {
			imageCount++
		}
	}
	if imageCount != 0 && imageCount != 2 {
		return nil, errors.New("screenshot requires exactly full and crop")
	}
	images := make(map[string]types.ContentBlock, 2)
	seenKeys := make(map[string]bool, 2)
	result := make([]types.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" {
			result = append(result, block)
			continue
		}
		if block.Type != "image" || h.uploads == nil {
			return nil, errors.New("bound attachments require owned screenshot images")
		}
		image, err := h.uploads.ownedArtifactScreenshot(uid, block.Payload)
		if err != nil {
			return nil, err
		}
		role := image.Payload["screenshot_role"].(string)
		key := image.Payload["file_key"].(string)
		if _, exists := images[role]; exists || seenKeys[key] {
			return nil, errors.New("duplicate screenshot role or upload")
		}
		images[role] = image
		seenKeys[key] = true
	}
	if len(images) == 0 {
		return result, nil
	}
	if len(images) != 2 {
		return nil, errors.New("screenshot requires exactly full and crop")
	}
	if images["crop"].Payload["width"].(int) > images["full"].Payload["width"].(int) || images["crop"].Payload["height"].(int) > images["full"].Payload["height"].(int) {
		return nil, fmt.Errorf("screenshot crop exceeds full viewport")
	}
	return append(result, images["full"], images["crop"]), nil
}
