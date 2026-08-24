// BEV（Bird's Eye View）投影：3D 点云 -> 2D 占据网格。
// 做法对齐 ROS 生态成熟实践（octomap_server / grid_map / pointcloud_to_occupancy_grid）：
// 先估计地面高度（直方图求模），再按「高度切片」[z0, z1]（障碍物高度带）投影——
// 切片内有点的格子记为占据。导出格式兼容 ROS map_server（PGM P5 + YAML），
// 车端导航栈可直接吃这份 2D 地图。
export interface BevGrid {
  nx: number;
  ny: number;
  cell: number;
  x0: number;
  y0: number;
  occ: Uint8Array;
  maxz: Float32Array;
  occupiedCells: number;
  ox0: number;
  ox1: number;
  oy0: number;
  oy1: number;
  fx0: number;
  fx1: number;
  fy0: number;
  fy1: number;
  groundZ: number;
  z0: number;
  z1: number;
}

// 直方图求模估计地面高度（0.5m 分箱，取点最多的箱）
export function estimateGroundZ(positions: number[]): number {
  const n = Math.floor(positions.length / 3);
  if (n === 0) return 0;
  let zmin = Infinity;
  let zmax = -Infinity;
  for (let i = 0; i < n; i++) {
    const z = positions[i * 3 + 2];
    if (z < zmin) zmin = z;
    if (z > zmax) zmax = z;
  }
  const bins = Math.max(1, Math.min(512, Math.ceil((zmax - zmin) / 0.5) || 1));
  const hist = new Uint32Array(bins);
  for (let i = 0; i < n; i++) {
    let b = Math.floor((positions[i * 3 + 2] - zmin) / 0.5);
    if (b < 0) b = 0;
    if (b >= bins) b = bins - 1;
    hist[b]++;
  }
  let best = 0;
  for (let b = 1; b < bins; b++) if (hist[b] > hist[best]) best = b;
  return zmin + (best + 0.5) * 0.5;
}

const MAX_SIDE = 1200; // 网格单边上限，防浏览器爆内存

export function buildBev(positions: number[], cell: number): BevGrid | null {
  const n = Math.floor(positions.length / 3);
  if (n < 3 || !(cell > 0)) return null;
  let x0 = Infinity;
  let x1 = -Infinity;
  let y0 = Infinity;
  let y1 = -Infinity;
  for (let i = 0; i < n; i++) {
    const x = positions[i * 3];
    const y = positions[i * 3 + 1];
    if (x < x0) x0 = x;
    if (x > x1) x1 = x;
    if (y < y0) y0 = y;
    if (y > y1) y1 = y;
  }
  if (!Number.isFinite(x0)) return null;
  let c = cell;
  while ((x1 - x0) / c > MAX_SIDE || (y1 - y0) / c > MAX_SIDE) c *= 2;
  const nx = Math.max(1, Math.ceil((x1 - x0) / c));
  const ny = Math.max(1, Math.ceil((y1 - y0) / c));
  const groundZ = estimateGroundZ(positions);
  const z0 = groundZ + 0.15;
  const z1 = groundZ + 1.8;
  const occ = new Uint8Array(nx * ny);
  const maxz = new Float32Array(nx * ny);
  let occupiedCells = 0;
  let minIx = nx;
  let minIy = ny;
  let maxIx = -1;
  let maxIy = -1;
  for (let i = 0; i < n; i++) {
    const z = positions[i * 3 + 2];
    if (z < z0 || z > z1) continue;
    let ix = Math.floor((positions[i * 3] - x0) / c);
    let iy = Math.floor((positions[i * 3 + 1] - y0) / c);
    if (ix < 0) ix = 0;
    if (ix >= nx) ix = nx - 1;
    if (iy < 0) iy = 0;
    if (iy >= ny) iy = ny - 1;
    const idx = iy * nx + ix;
    if (!occ[idx]) {
      occ[idx] = 1;
      occupiedCells++;
      maxz[idx] = z;
      if (ix < minIx) minIx = ix;
      if (ix > maxIx) maxIx = ix;
      if (iy < minIy) minIy = iy;
      if (iy > maxIy) maxIy = iy;
    } else if (z > maxz[idx]) {
      maxz[idx] = z;
    }
  }
  const ox0 = maxIx < 0 ? x0 : x0 + minIx * c;
  const ox1 = maxIx < 0 ? x1 : x0 + (maxIx + 1) * c;
  const oy0 = maxIy < 0 ? y0 : y0 + minIy * c;
  const oy1 = maxIy < 0 ? y1 : y0 + (maxIy + 1) * c;
  // 取景包围盒：占据格坐标做 2%~98% 百分位裁剪，去掉稀疏离群点
  let fx0 = ox0;
  let fx1 = ox1;
  let fy0 = oy0;
  let fy1 = oy1;
  if (occupiedCells >= 50) {
    const cx: number[] = [];
    const cy: number[] = [];
    for (let iy2 = 0; iy2 < ny; iy2++) {
      for (let ix2 = 0; ix2 < nx; ix2++) {
        if (occ[iy2 * nx + ix2]) {
          cx.push(x0 + (ix2 + 0.5) * c);
          cy.push(y0 + (iy2 + 0.5) * c);
        }
      }
    }
    cx.sort((a, b) => a - b);
    cy.sort((a, b) => a - b);
    const lo = Math.floor(cx.length * 0.02);
    const hi = Math.min(cx.length - 1, Math.ceil(cx.length * 0.98));
    fx0 = cx[lo];
    fx1 = cx[hi];
    fy0 = cy[lo];
    fy1 = cy[hi];
  }
  return {
    nx, ny, cell: c, x0, y0, occ, maxz, occupiedCells,
    ox0, ox1, oy0, oy1, fx0, fx1, fy0, fy1, groundZ, z0, z1,
  };
}

// 占据格按高度渐变着色（与 3D 静态点配色一致）
export function bevColor(t: number): [number, number, number] {
  return [0.05 + 0.4 * t, 0.16 + 0.6 * t, 0.35 + 0.6 * t];
}
