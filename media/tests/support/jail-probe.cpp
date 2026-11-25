// a stand-in for soloist that the jail tests run as /soloist: linked statically, since the jail's root holds no
// libraries. it reports what it finds of its process and its filesystem on stdout, one fact per line, or behaves as
// a lifecycle test needs
#include <dirent.h>
#include <fcntl.h>
#include <signal.h>
#include <sys/prctl.h>
#include <sys/resource.h>
#include <sys/stat.h>
#include <unistd.h>

#include <algorithm>
#include <cerrno>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <span>
#include <string>
#include <string_view>
#include <vector>

extern char** environ;

namespace {

// every fd an exec might have let through, up to the range the jail's sweep covers, checked before the probe opens
// any of its own
void reportDescriptors() {
  const auto limit = static_cast<int>(std::min(::sysconf(_SC_OPEN_MAX), 65536L));
  for (int fd = 3; fd < limit; ++fd)
    if (::fcntl(fd, F_GETFD) != -1)
      std::printf("fd %d\n", fd);
}

std::string_view kind(mode_t mode) {
  if (S_ISFIFO(mode))
    return "fifo";
  if (S_ISCHR(mode))
    return "chr";
  if (S_ISREG(mode))
    return "reg";
  if (S_ISSOCK(mode))
    return "sock";
  return "other";
}

void reportStdio() {
  struct stat in {};
  struct stat null {};
  const bool isNull = ::fstat(STDIN_FILENO, &in) == 0 && ::stat("/dev/null", &null) == 0 && S_ISCHR(in.st_mode) &&
                      in.st_rdev == null.st_rdev;
  std::printf("stdin %s\n", isNull ? "null" : "other");
  for (const auto& [name, fd] : {std::pair{"stdout", STDOUT_FILENO}, std::pair{"stderr", STDERR_FILENO}}) {
    struct stat info {};
    if (::fstat(fd, &info) == 0)
      std::printf("%s %.*s %llu\n", name, static_cast<int>(kind(info.st_mode).size()), kind(info.st_mode).data(),
                  static_cast<unsigned long long>(info.st_ino));
  }
  int nonblocking = 0;
  for (int fd = 0; fd < 3; ++fd)
    if ((::fcntl(fd, F_GETFL) & O_NONBLOCK) != 0)
      ++nonblocking;
  std::printf("stdio_nonblock %d\n", nonblocking);
}

void reportIds() {
  uid_t uid[3]{};
  gid_t gid[3]{};
  ::getresuid(&uid[0], &uid[1], &uid[2]);
  ::getresgid(&gid[0], &gid[1], &gid[2]);
  std::printf("uid %u %u %u\n", uid[0], uid[1], uid[2]);
  std::printf("gid %u %u %u\n", gid[0], gid[1], gid[2]);
  std::printf("groups %d\n", ::getgroups(0, nullptr));
  int signal = 0;
  ::prctl(PR_GET_PDEATHSIG, &signal, 0UL, 0UL, 0UL);
  std::printf("pdeathsig %d\n", signal);
}

// NoNewPrivs, CapEff, the blocked and the ignored signals, as the kernel reports them, then the nice value and the oom
// score
void reportStatus() {
  FILE* status = std::fopen("/proc/self/status", "re");
  if (status == nullptr)
    return;
  char line[256];
  while (std::fgets(line, sizeof line, status) != nullptr) {
    for (const std::string_view key : {"NoNewPrivs:", "CapEff:", "SigBlk:", "SigIgn:"}) {
      if (std::strncmp(line, key.data(), key.size()) != 0)
        continue;
      const char* value = line + key.size();
      while (*value == ' ' || *value == '\t')
        ++value;
      std::printf("%.*s %.*s\n", static_cast<int>(key.size() - 1), key.data(),
                  static_cast<int>(std::strcspn(value, "\n")), value);
    }
  }
  std::fclose(status);
  std::printf("nice %d\n", ::getpriority(PRIO_PROCESS, 0));
  FILE* oom = std::fopen("/proc/self/oom_score_adj", "re");
  int score = 0;
  if (oom != nullptr && std::fscanf(oom, "%d", &score) == 1)
    std::printf("oom_score_adj %d\n", score);
  if (oom != nullptr)
    std::fclose(oom);
}

void reportEnvironment() {
  for (char** entry = environ; *entry != nullptr; ++entry)
    std::printf("env %s\n", *entry);
  char cwd[4096];
  std::printf("cwd %s\n", ::getcwd(cwd, sizeof cwd) != nullptr ? cwd : "?");
}

// mount point, file system type and the mount's own ro or rw, from mountinfo's fields 5, 6 and the one after " - "
void reportMounts() {
  FILE* mounts = std::fopen("/proc/self/mountinfo", "re");
  if (mounts == nullptr)
    return;
  char line[4096];
  while (std::fgets(line, sizeof line, mounts) != nullptr) {
    std::vector<std::string> fields;
    for (char* field = std::strtok(line, " \n"); field != nullptr; field = std::strtok(nullptr, " \n"))
      fields.emplace_back(field);
    std::size_t separator = 6;
    while (separator < fields.size() && fields[separator] != "-")
      ++separator;
    if (separator + 1 >= fields.size())
      continue;
    const bool readOnly = fields[5] == "ro" || fields[5].starts_with("ro,");
    std::printf("mount %s %s %s\n", fields[4].c_str(), fields[separator + 1].c_str(), readOnly ? "ro" : "rw");
  }
  std::fclose(mounts);
}

void tryOpen(const char* path) {
  const int fd = ::open(path, O_RDONLY | O_CLOEXEC);
  if (fd >= 0) {
    std::printf("open %s ok\n", path);
    ::close(fd);
  } else {
    std::printf("open %s errno %d\n", path, errno);
  }
}

void readFirstLine(const char* path) {
  FILE* file = std::fopen(path, "re");
  char line[256] = "";
  if (file != nullptr) {
    if (std::fgets(line, sizeof line, file) == nullptr)
      line[0] = '\0';
    std::fclose(file);
  }
  std::printf("read %s %.*s\n", path, static_cast<int>(std::strcspn(line, "\n")), line);
}

std::string content(const char* path) {
  std::string text;
  const int fd = ::open(path, O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
  if (fd < 0)
    return text;
  char chunk[65536];
  for (ssize_t size; (size = ::read(fd, chunk, sizeof chunk)) > 0 && text.size() < (64u << 20);)
    text.append(chunk, static_cast<std::size_t>(size));
  ::close(fd);
  return text;
}

// every directory it can create a file in and every regular file it can open for writing (nothing is written to
// one), and every file holding one of tokens; /proc is the kernel's and is left out
void walk(const std::string& dir, const std::vector<std::string_view>& tokens) {
  const auto probe = (dir == "/" ? std::string() : dir) + "/.jail-probe-" + std::to_string(::getpid());
  const int created = ::open(probe.c_str(), O_CREAT | O_EXCL | O_WRONLY | O_CLOEXEC, 0600);
  if (created >= 0) {
    std::printf("writable %s\n", dir.c_str());
    ::close(created);
    ::unlink(probe.c_str());
  }
  DIR* listing = ::opendir(dir.c_str());
  if (listing == nullptr)
    return;
  std::vector<std::string> names;
  while (const dirent* entry = ::readdir(listing)) {
    const std::string_view name = entry->d_name;
    if (name != "." && name != "..")
      names.emplace_back(name);
  }
  ::closedir(listing);
  for (const auto& name : names) {
    const auto path = (dir == "/" ? std::string() : dir) + "/" + name;
    struct stat info {};
    if (path == "/proc" || ::lstat(path.c_str(), &info) != 0)
      continue;
    if (S_ISDIR(info.st_mode)) {
      walk(path, tokens);
    } else if (S_ISREG(info.st_mode)) {
      const int fd = ::open(path.c_str(), O_WRONLY | O_APPEND | O_CLOEXEC | O_NOFOLLOW);
      if (fd >= 0) {
        std::printf("writable %s\n", path.c_str());
        ::close(fd);
      }
      const auto text = content(path.c_str());
      for (const auto token : tokens)
        if (text.find(token) != std::string::npos)
          std::printf("token %.*s %s\n", static_cast<int>(token.size()), token.data(), path.c_str());
    }
  }
}

int report(std::span<char*> args) {
  reportDescriptors();
  reportStdio();
  reportIds();
  reportStatus();
  reportEnvironment();
  reportMounts();
  std::vector<std::string_view> tokens;
  bool walking = false;
  for (std::size_t i = 0; i < args.size(); ++i) {
    const std::string_view arg = args[i];
    if (arg == "--walk") {
      walking = true;
    } else if (i + 1 < args.size()) {
      if (arg == "--open")
        tryOpen(args[++i]);
      else if (arg == "--read")
        readFirstLine(args[++i]);
      else if (arg == "--token")
        tokens.emplace_back(args[++i]);
    }
  }
  if (walking)
    walk("/", tokens);
  return 0;
}

void ready() {
  std::puts("ready");
  std::fflush(stdout);
}

[[noreturn]] void waitForever() {
  for (;;)
    ::pause();
}

}  // namespace

// report [--open path]... [--read path]... [--walk] [--token text]... | exit <code> | wait | ignore-term | hang |
// spew <bytes> | print <text>
int main(int argc, char** argv) {
  const std::span<char*> args(argv, static_cast<std::size_t>(argc));
  const std::string_view mode = args.size() > 1 ? args[1] : "";
  if (mode == "report")
    return report(args.subspan(2));
  if (mode == "exit" && args.size() > 2)
    return std::atoi(args[2]);
  if (mode == "wait") {
    ready();
    waitForever();
  }
  if (mode == "ignore-term") {
    ::signal(SIGTERM, SIG_IGN);
    ready();
    waitForever();
  }
  if (mode == "hang")
    waitForever();
  if (mode == "spew" && args.size() > 2) {
    const std::string bytes(static_cast<std::size_t>(std::atol(args[2])), 'x');
    std::fwrite(bytes.data(), 1, bytes.size(), stdout);
    return 0;
  }
  if (mode == "print" && args.size() > 2) {
    std::puts(args[2]);
    return 0;
  }
  return 64;
}
