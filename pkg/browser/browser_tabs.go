package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

// ListTabs returns all open tabs.
func (m *Manager) ListTabs(ctx context.Context) ([]TabInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	b, err := m.browserLocked()
	if err != nil {
		return nil, err
	}

	pages, err := b.Pages()
	if err != nil {
		if m.remoteURL != "" {
			if reconnErr := m.reconnectLocked(); reconnErr != nil {
				return nil, fmt.Errorf("list pages: %w (reconnect also failed: %v)", err, reconnErr)
			}
			m.logger.Info("auto-reconnected to remote Chrome")
			b, err = m.browserLocked()
			if err != nil {
				return nil, err
			}
			pages, err = b.Pages()
			if err != nil {
				return nil, fmt.Errorf("list pages after reconnect: %w", err)
			}
		} else {
			return nil, fmt.Errorf("list pages: %w", err)
		}
	}

	tabs := make([]TabInfo, 0, len(pages))
	for _, p := range pages {
		info, err := p.Info()
		if err != nil || info == nil {
			continue
		}
		tid := string(p.TargetID)
		m.pages[tid] = p
		tabs = append(tabs, TabInfo{
			TargetID: tid,
			URL:      info.URL,
			Title:    info.Title,
		})
	}
	return tabs, nil
}

// OpenTab opens a new tab with the given URL.
// If maxPages is reached, the oldest idle page is closed first.
func (m *Manager) OpenTab(ctx context.Context, url string) (*TabInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Enforce max pages
	if m.maxPages > 0 {
		m.evictOldestIfOverLimitLocked()
	}

	b, err := m.browserLocked()
	if err != nil {
		return nil, err
	}

	page, err := b.Page(proto.TargetCreateTarget{URL: url})
	if err != nil {
		return nil, fmt.Errorf("open tab: %w", err)
	}

	// Watchdog: close page on ctx cancel to unblock WaitStable CDP call.
	stopWatchdog := watchPageClose(ctx, page)
	if err := page.WaitStable(300 * time.Millisecond); err != nil {
		stopWatchdog()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("wait stable: %w", err)
	}
	stopWatchdog()
	info, _ := page.Info()
	tid := string(page.TargetID)
	m.pages[tid] = page
	m.touchPageLocked(tid)

	// Set up console listener
	m.setupConsoleListener(page, tid)

	tab := &TabInfo{TargetID: tid, URL: url}
	if info != nil {
		tab.URL = info.URL
		tab.Title = info.Title
	}
	return tab, nil
}

// evictOldestIfOverLimitLocked closes the oldest idle page if at or over maxPages.
// Must be called with mu held.
func (m *Manager) evictOldestIfOverLimitLocked() {
	if len(m.pages) < m.maxPages {
		return
	}

	// Find the oldest page by lastUsed
	var oldestID string
	var oldestTime time.Time
	for tid := range m.pages {
		lu, ok := m.pageLastUsed[tid]
		if !ok {
			oldestID = tid
			break
		}
		if oldestID == "" || lu.Before(oldestTime) {
			oldestID = tid
			oldestTime = lu
		}
	}

	if oldestID == "" {
		return
	}

	if page, ok := m.pages[oldestID]; ok {
		_ = page.Close()
	}
	delete(m.pages, oldestID)
	delete(m.console, oldestID)
	delete(m.pageLastUsed, oldestID)
	m.refs.Remove(oldestID)
	m.logger.Info("evicted oldest page (max pages reached)", "targetId", oldestID)
}

// FocusTab activates a tab.
func (m *Manager) FocusTab(ctx context.Context, targetID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	page, err := m.getPage(targetID)
	if err != nil {
		return err
	}

	_, err = page.Activate()
	return err
}

// CloseTab closes a tab.
func (m *Manager) CloseTab(ctx context.Context, targetID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	page, err := m.getPage(targetID)
	if err != nil {
		return err
	}

	delete(m.pages, targetID)
	delete(m.console, targetID)
	delete(m.pageLastUsed, targetID)
	m.refs.Remove(targetID)
	return page.Close()
}

// ConsoleMessages returns captured console messages for a tab.
func (m *Manager) ConsoleMessages(ctx context.Context, targetID string) []ConsoleMessage {
	m.mu.Lock()
	defer m.mu.Unlock()

	msgs := m.console[targetID]
	if msgs == nil {
		return []ConsoleMessage{}
	}

	// Return copy and clear
	result := make([]ConsoleMessage, len(msgs))
	copy(result, msgs)
	m.console[targetID] = nil
	return result
}
