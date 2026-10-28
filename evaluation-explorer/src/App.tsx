// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useCallback, useEffect, useRef, useState } from 'react';
import { BrowserRouter, Link, Navigate, Route, Routes } from 'react-router-dom';
import { useFeedStatus, useConnection, useStoreState, evalStore } from './state/store';
import { startFeed, stopFeed } from './state/startup';
import { ActiveCampaignBar } from './components/ActiveCampaignBar';
import { ErrorBanner, FreshnessBadge } from './components/shared';
import { AboutView } from './views/AboutView';
import { SponsorsView } from './views/SponsorsView';
import { OverviewView } from './views/OverviewView';
import { LeaderboardView } from './views/LeaderboardView';
import { ModelsView } from './views/ModelsView';
import { ModelDetailView } from './views/ModelDetailView';
import { EvaluationsView } from './views/EvaluationsView';
import { EvaluationDetailView } from './views/EvaluationDetailView';
import { AssignmentDetailView } from './views/AssignmentDetailView';
import { MethodologyView } from './views/MethodologyView';
import { TasksView } from './views/TasksView';
import { ThemeToggle } from './components/ThemeToggle';
import { NavDrawer } from './components/NavDrawer';
import { AppFooter } from './components/AppFooter';
import brandWordmarkUrl from './assets/brand/opendevops-logo-plus-title-text.png';
import brandLogoUrl from './assets/brand/opendevops-logo-only.png';

function Shell() {
  const [drawerOpen, setDrawerOpen] = useState(true);
  const drawerButtonRef = useRef<HTMLButtonElement>(null);
  const closeDrawer = useCallback(() => {
    setDrawerOpen(false);
    drawerButtonRef.current?.focus();
  }, []);
  const feedStatus = useFeedStatus();
  const connection = useConnection();
  const errors = useStoreState((state) => state.errors);

  useEffect(() => {
    // Start the feed. The startup orchestrator retains only accepted mirror
    // data on transport errors. When runtime.json points at a live mirror,
    // bootstrap/history/snapshot/SSE connect.
    void startFeed();
    return () => stopFeed();
  }, []);

  useEffect(() => {
    if (!drawerOpen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !event.defaultPrevented) closeDrawer();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [drawerOpen, closeDrawer]);

  return (
    <div className={`app-shell connection-${connection}${drawerOpen ? '' : ' is-drawer-closed'}`}>
      <a
        className="skip-link"
        href="#main"
        onClick={(event) => {
          event.preventDefault();
          document.getElementById('main')?.focus();
        }}
      >
        Skip to content
      </a>
      <header className="app-header">
        <div className="header-primary">
          <div className="header-leading">
            <button
              ref={drawerButtonRef}
              type="button"
              className={`nav-hamburger-btn${drawerOpen ? ' is-active' : ''}`}
              onClick={() => setDrawerOpen((open) => !open)}
              aria-label={drawerOpen ? 'Close navigation drawer' : 'Open navigation drawer'}
              aria-expanded={drawerOpen}
              aria-controls="navigation-drawer"
              title="Navigation and runs"
            >
              <span className="nav-hamburger-icon" aria-hidden="true">
                <span className="hamburger-line line-1" />
                <span className="hamburger-line line-2" />
                <span className="hamburger-line line-3" />
              </span>
            </button>
            <Link className="header-brand" to="/" aria-label="OpenDevOps.ai home">
              <img className="header-brand-wordmark" src={brandWordmarkUrl} alt="" />
              <img className="header-brand-logo" src={brandLogoUrl} alt="" />
            </Link>
          </div>
          <div className="header-campaign-slot">
            <ActiveCampaignBar />
          </div>
          <div className="header-utilities">
            <ThemeToggle />
            <Link className="header-cta header-cta-sponsor" to="/sponsors">
              Sponsor
            </Link>
            {feedStatus ? <FreshnessBadge feedStatus={feedStatus} /> : null}
          </div>
        </div>
      </header>
      <div className="app-workspace">
        <NavDrawer isOpen={drawerOpen} onClose={closeDrawer} />
        <div className="app-content">
          <ErrorBanner errors={errors} onDismiss={() => evalStore.clearErrors()} />
          <main id="main" className="app-main" tabIndex={-1}>
            <Routes>
              <Route path="/" element={<OverviewView />} />
              <Route path="/leaderboard" element={<LeaderboardView />} />
              <Route path="/models" element={<ModelsView />} />
              <Route path="/models/:datasetId?/:variantId" element={<ModelDetailView />} />
              <Route path="/evaluations" element={<EvaluationsView />} />
              <Route path="/evaluations/:datasetId?/:runId" element={<EvaluationDetailView />} />
              <Route path="/evaluations/:datasetId?/:runId/assignments/:assignmentId" element={<AssignmentDetailView />} />
              <Route path="/failures" element={<Navigate to="/evaluations" replace />} />
              <Route path="/about" element={<AboutView />} />
              <Route path="/sponsors" element={<SponsorsView />} />
              <Route path="/tasks" element={<TasksView />} />
              <Route path="/tasks/:taskId" element={<TasksView />} />
              <Route path="/methodology" element={<MethodologyView />} />
            </Routes>
          </main>
        </div>
      </div>
      <AppFooter />
    </div>
  );
}

export function App() {
  return (
    <BrowserRouter>
      <Shell />
    </BrowserRouter>
  );
}
