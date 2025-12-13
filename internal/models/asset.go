package models

type UploadAssetRequest struct {
	FileName    string `json:"fileName" binding:"required" db:"file_name"`
	ContentType string `json:"contentType" binding:"required" db:"content_type"`
	Size        int64  `json:"size" db:"size"`
}

type UploadAssetResponse struct {
	UploadUrl  string            `json:"uploadUrl"`
	AssetID    string            `json:"assetId"`
	Method     string            `json:"method"`
	Headers    map[string]string `json:"headers"`
	ObjectPath string            `json:"objectPath"`
	PublicUrl  string            `json:"publicUrl"`
	ExpiresAt  int64             `json:"expiresAt"`
}
