export type Density = "comfortable" | "compact";

export interface SavedView {
  id: string;
  name: string;
  entity: string;
  columns: string[];
  density: Density;
}

export interface Personalization {
  views: SavedView[];
  activeViewId?: string;
}

export function saveView(state: Personalization, view: SavedView): Personalization {
  const rest = state.views.filter((v) => v.id !== view.id);
  return { views: [...rest, view], activeViewId: view.id };
}

export function applyView(state: Personalization, id: string): Personalization {
  if (!state.views.some((v) => v.id === id)) {
    return state;
  }
  return { ...state, activeViewId: id };
}

export function activeView(state: Personalization): SavedView | undefined {
  return state.views.find((v) => v.id === state.activeViewId);
}
