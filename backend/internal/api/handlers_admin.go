package api

import (
	"fmt"
	"net/http"
	"strings"

	"dbharbor/backend/internal/store"
)

func (s *Server) handleUpdateProfile(c *Context) {
	var req struct {
		DisplayName string `json:"displayName"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid profile parameters"})
		return
	}
	dname := strings.TrimSpace(req.DisplayName)
	if len(dname) > 64 {
		dname = dname[:64]
	}
	if err := s.store.UpdateUserProfile(c.UserID(), dname); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	s.store.AddAuditLog("PROFILE_UPDATED", c.Username(), c.ClientIP(), c.Request.UserAgent(), fmt.Sprintf("Updated display name to %q", dname))
	c.JSON(http.StatusOK, H{"message": "Profile updated successfully", "displayName": dname})
}

func (s *Server) handleAdminGetLoginBranding(c *Context) {
	if !c.RequireAdmin() {
		return
	}
	c.JSON(http.StatusOK, H{"branding": s.store.GetLoginBranding()})
}

func (s *Server) handleAdminSaveLoginBranding(c *Context) {
	if !c.RequireAdmin() {
		return
	}
	var in store.LoginBrandingConfig
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid login branding configuration payload"})
		return
	}
	saved, err := s.store.SaveLoginBranding(in)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	s.store.AddAuditLog(
		"LOGIN_BRANDING_UPDATED",
		c.Username(),
		c.ClientIP(),
		c.Request.UserAgent(),
		fmt.Sprintf("Updated login page branding (iconMode=%s, hasTitle=%v, hasHeading=%v)", saved.IconMode, saved.PageTitle != "", saved.Heading != ""),
	)
	c.JSON(http.StatusOK, H{"message": "Login page appearance updated", "branding": saved})
}

func (s *Server) handleAdminListUsers(c *Context) {
	if !c.RequireAdmin() {
		return
	}
	users, err := s.store.ListUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"users": users})
}

func (s *Server) handleAdminCreateUser(c *Context) {
	if !c.RequireAdmin() {
		return
	}
	var in store.CreateUserInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid user creation payload"})
		return
	}
	created, err := s.store.CreateUser(in)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	s.store.AddAuditLog(
		"USER_CREATED",
		c.Username(),
		c.ClientIP(),
		c.Request.UserAgent(),
		fmt.Sprintf("Created user %q (role=%s, connScope=%s)", created.Username, created.Role, created.ConnScope),
	)
	c.JSON(http.StatusOK, created)
}

func (s *Server) handleAdminUpdateUser(c *Context) {
	if !c.RequireAdmin() {
		return
	}
	uid := strings.TrimSpace(c.Param("uid"))
	if uid == "" {
		c.JSON(http.StatusBadRequest, H{"error": "User ID is required"})
		return
	}
	var in store.UpdateUserInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid user update payload"})
		return
	}
	// Prevent an admin from disabling their own currently signed-in account
	if uid == c.UserID() && in.Disabled != nil && *in.Disabled {
		c.JSON(http.StatusBadRequest, H{"error": "Cannot disable your own currently signed-in account"})
		return
	}
	updated, err := s.store.UpdateUser(uid, in)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	event := "USER_UPDATED"
	if updated.Disabled {
		event = "USER_DISABLED"
	}
	s.store.AddAuditLog(
		event,
		c.Username(),
		c.ClientIP(),
		c.Request.UserAgent(),
		fmt.Sprintf("Updated user %q (role=%s, disabled=%v, canCreateConn=%v, connScope=%s)", updated.Username, updated.Role, updated.Disabled, updated.CanCreateConn, updated.ConnScope),
	)
	c.JSON(http.StatusOK, updated)
}

func (s *Server) handleAdminDeleteUser(c *Context) {
	if !c.RequireAdmin() {
		return
	}
	uid := strings.TrimSpace(c.Param("uid"))
	if uid == "" {
		c.JSON(http.StatusBadRequest, H{"error": "User ID is required"})
		return
	}
	target, _ := s.store.GetUserByID(uid)
	targetName := uid
	if target != nil {
		targetName = target.Username
	}
	sids, privConnIDs, err := s.store.DeleteUser(uid, c.UserID())
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	if s.tokens != nil {
		for _, sid := range sids {
			s.tokens.RevokeSessionInMemory(sid, 0)
		}
	}
	for _, cid := range privConnIDs {
		s.pool.Evict(cid)
		s.invalidateConnCache(cid)
	}
	s.store.AddAuditLog(
		"USER_DELETED",
		c.Username(),
		c.ClientIP(),
		c.Request.UserAgent(),
		fmt.Sprintf("Deleted user %q and cleaned %d private connection(s)", targetName, len(privConnIDs)),
	)
	c.JSON(http.StatusOK, H{"message": "User deleted successfully"})
}

func (s *Server) handleAdminResetPassword(c *Context) {
	if !c.RequireAdmin() {
		return
	}
	uid := strings.TrimSpace(c.Param("uid"))
	var req struct {
		NewPassword         string `json:"newPassword"`
		ForcePasswordChange bool   `json:"forcePasswordChange"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.NewPassword) < 8 {
		c.JSON(http.StatusBadRequest, H{"error": "New password must be at least 8 characters"})
		return
	}
	target, err := s.store.GetUserByID(uid)
	if err != nil {
		c.JSON(http.StatusNotFound, H{"error": "User not found"})
		return
	}
	sids, err := s.store.AdminResetPassword(uid, req.NewPassword, req.ForcePasswordChange)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	if s.protector != nil {
		s.protector.ResetUser(target.Username)
	}
	if s.tokens != nil {
		for _, sid := range sids {
			s.tokens.RevokeSessionInMemory(sid, 0)
		}
	}
	s.store.AddAuditLog(
		"ADMIN_RESET_PASSWORD",
		c.Username(),
		c.ClientIP(),
		c.Request.UserAgent(),
		fmt.Sprintf("Reset password for user %q and revoked %d session(s)", target.Username, len(sids)),
	)
	c.JSON(http.StatusOK, H{"message": "User password reset successfully", "revokedSessions": len(sids)})
}

func (s *Server) handleAdminReset2FA(c *Context) {
	if !c.RequireAdmin() {
		return
	}
	uid := strings.TrimSpace(c.Param("uid"))
	target, err := s.store.GetUserByID(uid)
	if err != nil {
		c.JSON(http.StatusNotFound, H{"error": "User not found"})
		return
	}
	if err := s.store.AdminReset2FA(uid); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	s.store.AddAuditLog(
		"ADMIN_RESET_2FA",
		c.Username(),
		c.ClientIP(),
		c.Request.UserAgent(),
		fmt.Sprintf("Reset TOTP two-factor authentication for user %q", target.Username),
	)
	c.JSON(http.StatusOK, H{"message": "Two-factor authentication reset for user"})
}

func (s *Server) handleAdminRevokeUserSessions(c *Context) {
	if !c.RequireAdmin() {
		return
	}
	uid := strings.TrimSpace(c.Param("uid"))
	target, err := s.store.GetUserByID(uid)
	if err != nil {
		c.JSON(http.StatusNotFound, H{"error": "User not found"})
		return
	}
	sids, err := s.store.RevokeAllUserSessions(uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	if s.tokens != nil {
		for _, sid := range sids {
			s.tokens.RevokeSessionInMemory(sid, 0)
		}
	}
	s.store.AddAuditLog(
		"ADMIN_REVOKE_SESSIONS",
		c.Username(),
		c.ClientIP(),
		c.Request.UserAgent(),
		fmt.Sprintf("Force-revoked %d session(s) for user %q", len(sids), target.Username),
	)
	c.JSON(http.StatusOK, H{"message": fmt.Sprintf("Revoked %d active session(s)", len(sids)), "revoked": len(sids)})
}
