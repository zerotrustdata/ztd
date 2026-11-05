// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { NavLink } from 'react-router-dom';
import { RunsPanel } from './RunsPanel';
import { isOverlayDrawerViewport } from '../utils/viewport';

interface NavItemConfig {
  to: string;
  label: string;
  description: string;
}

const NAV_ITEMS: NavItemConfig[] = [
  { to: '/', label: 'Live', description: 'Real-time campaign feed & telemetry' },
  { to: '/leaderboard', label: 'Leaderboard', description: 'Role leaders & composite rankings' },
  { to: '/evaluations', label: 'Evals', description: 'Campaign runs & assignment outcomes' },
  { to: '/tasks', label: 'Tasks', description: 'Benchmark scenarios & task suite' },
  { to: '/models', label: 'Models', description: 'Evaluated models & scorecards' },
  { to: '/methodology', label: 'Docs', description: 'Methodology & governance specifications' },
  { to: '/about', label: 'About', description: 'Evaluation goals, project & builder' },
  { to: '/sponsors', label: 'Sponsors', description: 'Top sponsors & project sustainability' },
];

interface NavDrawerProps {
  isOpen: boolean;
  onClose: () => void;
}

export function NavDrawer({ isOpen, onClose }: NavDrawerProps) {
  return (
    <aside
      id="navigation-drawer"
      className="nav-drawer"
      aria-label="Navigation and runs"
      inert={!isOpen}
      onClick={(event) => {
        // An overlay would otherwise keep covering the page the visitor just opened.
        if (isOverlayDrawerViewport() && (event.target as Element).closest('a')) onClose();
      }}
    >
      <div className="nav-drawer-header">
        <span className="nav-drawer-kicker">Navigation</span>
        <button
          type="button"
          className="nav-drawer-close-btn"
          onClick={onClose}
          aria-label="Close navigation drawer"
        >
          ✕
        </button>
      </div>
      <nav className="nav-drawer-items" aria-label="Primary navigation">
        {NAV_ITEMS.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            end={item.to === '/'}
            title={item.description}
            className={({ isActive }) => `nav-drawer-item${isActive ? ' active' : ''}`}
          >
            <span className="nav-drawer-item-label">{item.label}</span>
          </NavLink>
        ))}
      </nav>
      <RunsPanel />
    </aside>
  );
}
