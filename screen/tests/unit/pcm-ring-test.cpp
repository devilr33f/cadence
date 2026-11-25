#include "player/pcm-ring.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <sys/mman.h>
#include <unistd.h>

using namespace cadence::screen::player;

TEST_CASE("the ring's memfd holds the header ao_cadence expects, sealed against resizing") {
  std::string error;
  auto ring = PcmRing::create(8192, &error);
  INFO(error);
  REQUIRE(ring);
  const auto* h = ring->header();
  CHECK(h->magic == CADENCE_RING_MAGIC);
  CHECK(h->version == CADENCE_RING_VERSION);
  CHECK(h->rate == 48000);
  CHECK(h->channels == 2);
  CHECK(h->capacity == 8192);
  CHECK(h->data_offset == CADENCE_RING_DATA_OFFSET);
  CHECK(h->write_pos == 0);
  CHECK(ring->bytes() == 256u + 8192u * 2u * 4u);

  // what the reader (audiod) does: map the same fd and see the same header
  void* other = ::mmap(nullptr, ring->bytes(), PROT_READ, MAP_SHARED, ring->ringFd(), 0);
  REQUIRE(other != MAP_FAILED);
  CHECK(static_cast<const cadence_ring_header*>(other)->magic == CADENCE_RING_MAGIC);
  ::munmap(other, ring->bytes());

  CHECK(::ftruncate(ring->ringFd(), 1 << 20) != 0);
  const int seals = ::fcntl(ring->ringFd(), F_GET_SEALS);
  CHECK((seals & F_SEAL_GROW) != 0);
  CHECK((seals & F_SEAL_SHRINK) != 0);
  CHECK(ring->eventFd() >= 0);
}

TEST_CASE("a ring's capacity is a power of two") {
  std::string error;
  CHECK_FALSE(PcmRing::create(1000, &error));
  CHECK(error == "capacity");
  CHECK_FALSE(PcmRing::create(0, &error));
}
