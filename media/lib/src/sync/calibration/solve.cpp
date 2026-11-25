#include "sync/calibration/solve.h"

#include <algorithm>
#include <cmath>

namespace cadence::sync::calibration {

Solution solveV2(const Plan& plan, const std::vector<std::vector<HeardV2>>& heard, const SolverConfig& config) {
  Solution result;
  const auto n = plan.members.size();
  result.members.resize(n);
  for (std::size_t i = 0; i < n; ++i)
    result.members[i].id = plan.members[i];
  if (n < 2) {
    result.code = ResultCode::TooFew;
    return result;
  }
  auto at = [&](std::size_t recorder, std::size_t source) -> const HeardV2* {
    return recorder < heard.size() && source < heard[recorder].size() ? &heard[recorder][source] : nullptr;
  };
  auto absolute = [&](std::size_t recorder, std::size_t source) -> const HeardV2* {
    const auto* value = at(recorder, source);
    return value != nullptr && value->code == ArrivalCodeV2::Ok ? value : nullptr;
  };
  auto unstable = [&](std::size_t recorder, std::size_t source) {
    const auto* value = at(recorder, source);
    return value != nullptr && value->code == ArrivalCodeV2::Unstable;
  };
  auto isolated = [&](std::size_t i) {
    for (std::size_t j = 0; j < n; ++j)
      if (j != i && (unstable(j, i) || unstable(i, j)))
        return MemberCode::Unstable;
    return MemberCode::Isolated;
  };

  std::vector<bool> self(n), linked(n);
  for (std::size_t i = 0; i < n; ++i) {
    self[i] = absolute(i, i) != nullptr;
    if (!self[i])
      result.members[i].code = unstable(i, i) ? MemberCode::Unstable : MemberCode::NoSelf;
  }
  struct Pair {
    std::size_t i, j;
    double differenceNs;
    double weight;
    double confidence;
    bool relative;
  };
  std::vector<Pair> pairs;
  double largestWeight = 0;
  for (std::size_t i = 0; i < n; ++i) {
    for (std::size_t j = i + 1; j < n; ++j) {
      const auto* ij = at(j, i);
      const auto* ji = at(i, j);
      if (ij == nullptr || ji == nullptr)
        continue;
      Pair pair{i, j, 0, 0, 0, false};
      if (ij->hasRelative && ji->hasRelative) {
        pair.relative = true;
        pair.differenceNs = (ij->relativeNs - ji->relativeNs) / 2.0;
        const double a = std::max(ij->relativeSpreadNs, 10'000.0);
        const double b = std::max(ji->relativeSpreadNs, 10'000.0);
        pair.weight = 1.0 / (a * a + b * b);
        pair.confidence = std::min(ij->confidence, ji->confidence);
      } else {
        const auto* aij = absolute(j, i);
        const auto* aji = absolute(i, j);
        const auto* si = absolute(i, i);
        const auto* sj = absolute(j, j);
        if (aij == nullptr || aji == nullptr || si == nullptr || sj == nullptr)
          continue;
        const double first = static_cast<double>(aij->tNs - plan.slotNs[i]);
        const double second = static_cast<double>(aji->tNs - plan.slotNs[j]);
        const double ownI = static_cast<double>(si->tNs - plan.slotNs[i]);
        const double ownJ = static_cast<double>(sj->tNs - plan.slotNs[j]);
        pair.differenceNs = ((first - second) + (ownI - ownJ)) / 2.0;
        const double a = std::max(aij->sigmaNs, 10'000.0);
        const double b = std::max(aji->sigmaNs, 10'000.0);
        pair.weight = 1.0 / (a * a + b * b);
        pair.confidence = std::min({aij->confidence, aji->confidence, si->confidence, sj->confidence});
      }
      largestWeight = std::max(largestWeight, pair.weight);
      linked[i] = linked[j] = true;
      pairs.push_back(pair);
    }
  }

  std::vector<int> component(n, -1);
  int components = 0;
  for (std::size_t start = 0; start < n; ++start) {
    if (component[start] >= 0 || (!self[start] && !linked[start]))
      continue;
    std::vector<std::size_t> stack{start};
    component[start] = components;
    while (!stack.empty()) {
      const auto u = stack.back();
      stack.pop_back();
      for (const auto& pair : pairs) {
        const auto v = pair.i == u ? pair.j : pair.j == u ? pair.i : n;
        if (v < n && component[v] < 0) {
          component[v] = components;
          stack.push_back(v);
        }
      }
    }
    ++components;
  }
  int chosen = -1;
  std::size_t chosenSize = 0;
  for (int c = 0; c < components; ++c) {
    const auto size = static_cast<std::size_t>(std::count(component.begin(), component.end(), c));
    if (size > chosenSize) {
      chosen = c;
      chosenSize = size;
    }
  }
  if (chosenSize < 2) {
    for (std::size_t i = 0; i < n; ++i)
      if (self[i] || linked[i])
        result.members[i].code = isolated(i);
    result.code = ResultCode::Incomplete;
    return result;
  }

  std::vector<std::size_t> nodes;
  std::vector<int> index(n, -1);
  for (std::size_t i = 0; i < n; ++i) {
    if (component[i] == chosen) {
      index[i] = static_cast<int>(nodes.size());
      nodes.push_back(i);
      result.members[i].code = MemberCode::Ok;
    } else if (self[i] || linked[i]) {
      result.members[i].code = isolated(i);
    }
  }

  const auto count = nodes.size();
  std::vector<std::vector<double>> a(count, std::vector<double>(count + 1, 1.0));
  for (auto& row : a)
    row[count] = 0;
  for (const auto& pair : pairs) {
    if (index[pair.i] < 0 || index[pair.j] < 0)
      continue;
    const auto u = static_cast<std::size_t>(index[pair.i]);
    const auto v = static_cast<std::size_t>(index[pair.j]);
    const double weight = pair.weight / largestWeight;
    a[u][u] += weight;
    a[v][v] += weight;
    a[u][v] -= weight;
    a[v][u] -= weight;
    a[u][count] += weight * pair.differenceNs;
    a[v][count] -= weight * pair.differenceNs;
  }
  for (std::size_t col = 0; col < count; ++col) {
    std::size_t pivot = col;
    for (std::size_t row = col + 1; row < count; ++row)
      if (std::abs(a[row][col]) > std::abs(a[pivot][col]))
        pivot = row;
    std::swap(a[col], a[pivot]);
    for (std::size_t row = 0; row < count; ++row) {
      if (row == col || a[col][col] == 0)
        continue;
      const double factor = a[row][col] / a[col][col];
      for (std::size_t c = col; c <= count; ++c)
        a[row][c] -= factor * a[col][c];
    }
  }
  std::vector<double> offsets(count);
  for (std::size_t row = 0; row < count; ++row)
    offsets[row] = a[row][row] != 0 ? a[row][count] / a[row][row] : 0;

  double residual = 0, weights = 0;
  std::vector<double> confidence(count), weightSum(count);
  for (const auto& pair : pairs) {
    if (index[pair.i] < 0 || index[pair.j] < 0)
      continue;
    const auto u = static_cast<std::size_t>(index[pair.i]);
    const auto v = static_cast<std::size_t>(index[pair.j]);
    const double error = offsets[u] - offsets[v] - pair.differenceNs;
    residual += pair.weight * error * error;
    weights += pair.weight;
    confidence[u] = std::max(confidence[u], pair.confidence);
    confidence[v] = std::max(confidence[v], pair.confidence);
    weightSum[u] += pair.weight;
    weightSum[v] += pair.weight;
    const auto* ij = at(pair.j, pair.i);
    const auto* ji = at(pair.i, pair.j);
    for (const auto k : {pair.i, pair.j}) {
      auto& member = result.members[k];
      for (const auto* arrival : {ij, ji}) {
        member.spreadNs = std::max(member.spreadNs, pair.relative ? arrival->relativeSpreadNs : arrival->spreadNs);
        member.rounds = member.rounds == 0 ? arrival->rounds : std::min(member.rounds, arrival->rounds);
      }
    }
  }
  result.residualNs = weights > 0 ? std::sqrt(residual / weights) : 0;
  for (std::size_t k = 0; k < count; ++k) {
    auto& member = result.members[nodes[k]];
    member.offsetNs = offsets[k];
    member.confidence = confidence[k];
    member.ci95Ns = weightSum[k] > 0 ? 1.96 / std::sqrt(weightSum[k]) : 0;
  }
  const bool all = std::all_of(result.members.begin(), result.members.end(),
                               [](const MemberOffset& member) { return member.code == MemberCode::Ok; });
  result.code = !all                                        ? ResultCode::Incomplete
                : result.residualNs > config.maxResidualNs ? ResultCode::Inconsistent
                                                            : ResultCode::Ok;
  return result;
}

}  // namespace cadence::sync::calibration
