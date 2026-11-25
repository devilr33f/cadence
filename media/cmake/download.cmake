# cadence_download(<url> <file> <sha256> <status var>): a pinned archive fetched in up to three attempts, each given up
# after 90 s in all (a stalled connect counts no bytes, so the inactivity limit alone never ends it) or 30 s without a
# byte: a connection that stalls (seen on ci's kata runner) fails in a minute or so, not in curl's 5 min connect timeout,
# and the next attempt goes through. the hash is checked here, not by file(DOWNLOAD), whose failed download with an
# expected hash is a configure error even when a later attempt succeeds. the status is the last attempt's
function(cadence_download url file sha256 out)
  foreach(attempt RANGE 1 3)
    file(DOWNLOAD "${url}" "${file}" TLS_VERIFY ON INACTIVITY_TIMEOUT 30 TIMEOUT 90 STATUS status)
    list(GET status 0 code)
    if(code EQUAL 0)
      file(SHA256 "${file}" got)
      if("${got}" STREQUAL "${sha256}")
        break()
      endif()
      set(status "1;\"sha256 ${got}, want ${sha256}\"")
    endif()
    message(STATUS "${url}: attempt ${attempt} failed: ${status}")
    file(REMOVE "${file}")
  endforeach()
  set(${out} "${status}" PARENT_SCOPE)
endfunction()
